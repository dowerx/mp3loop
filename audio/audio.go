// Package audio reads MP3 files and streams their frames in real time.
//
// A file is memory mapped and its frames indexed once via Audio.Load, after which any
// number of goroutines may stream from it concurrently. Playback position is derived
// from the wall clock rather than from the client, so every listener hears the track
// from the same point at the same moment.
package audio

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

// epoch is the reference point for mapping wall clock time to a position in the track.
var epoch = time.Unix(0, 0)

// frame is a single indexed MPEG audio frame within the mapped file. offset and length
// locate its bytes in Audio.bytes, while sampleOffset is its position in the track
// measured in samples from the first frame.
type frame struct {
	offset       int
	length       int
	sampleOffset int64

	mpegVersion int
	layer       int
	bitrate     int
	sampleRate  int
	padding     bool
	samples     int
	channels    int
}

// Parse decodes a four byte MPEG frame header and fills in the frame's derived fields,
// with offset recorded as the frame's byte position in the file. It reports false if h
// is not a supported header, in which case f is left in an unspecified state and the
// caller should treat the bytes as unparseable and keep scanning.
func (f *frame) Parse(h [4]byte, offset int) bool {
	x := uint32(h[0])<<24 |
		uint32(h[1])<<16 |
		uint32(h[2])<<8 |
		uint32(h[3])

	if (x>>21)&0x7ff != 0x7ff {
		return false
	}

	versionBits := (x >> 19) & 0x3
	layerBits := (x >> 17) & 0x3
	bitrateIndex := (x >> 12) & 0xf
	sampleIndex := (x >> 10) & 0x3
	padding := ((x >> 9) & 1) != 0

	switch versionBits {
	case 3:
		f.mpegVersion = 1
	case 2:
		f.mpegVersion = 2
	case 0:
		f.mpegVersion = 25
	default:
		return false
	}

	switch layerBits {
	case 1:
		f.layer = 3
	case 2:
		f.layer = 2
	case 3:
		f.layer = 1
	default:
		return false
	}

	if f.layer != 3 {
		return false
	}

	bitrateTable := map[int][]int{
		1:  {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
		2:  {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		25: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	}

	sampleTable := map[int][]int{
		1:  {44100, 48000, 32000, 0},
		2:  {22050, 24000, 16000, 0},
		25: {11025, 12000, 8000, 0},
	}

	f.bitrate = bitrateTable[f.mpegVersion][bitrateIndex]
	if f.bitrate == 0 {
		return false
	}

	f.sampleRate = sampleTable[f.mpegVersion][sampleIndex]
	if f.sampleRate == 0 {
		return false
	}

	multiplier := 144
	f.samples = 1152

	if f.mpegVersion != 1 {
		multiplier = 72
		f.samples = 576
	}

	f.length = multiplier * f.bitrate * 1000 / f.sampleRate

	if padding {
		f.length++
	}

	f.padding = padding
	f.offset = offset

	f.channels = 2
	if ((x >> 6) & 0x3) == 3 {
		f.channels = 1
	}

	return true
}

// Audio is a memory mapped MP3 file with its frames indexed for random access. Frames
// are read concurrently by any number of streaming goroutines, so a single Audio can be
// shared across listeners without locking.
type Audio struct {
	bytes  []byte
	frames []frame

	fd *os.File
}

// Close unmaps the file and closes the underlying descriptor. It must be called once
// the Audio is no longer being read from, otherwise streaming goroutines may fault on
// unmapped memory.
func (a *Audio) Close() {
	unix.Munmap(a.bytes)
	a.fd.Close()
}

// Load memory maps mp3File and indexes its MPEG frames, skipping any leading ID3v2 tag.
// Unparseable bytes are scanned past rather than treated as an error, so files with
// trailing tags still load. It returns an error if the file cannot be mapped or if no
// frames are found. Close must be called to release the mapping.
func (a *Audio) Load(mp3File string) (err error) {
	slog.Info("loading audio", "path", mp3File)

	a.fd, err = os.OpenFile(mp3File, os.O_RDONLY, 0)
	if err != nil {
		return err
	}

	info, err := a.fd.Stat()
	if err != nil {
		return err
	}

	a.bytes, err = unix.Mmap(int(a.fd.Fd()), 0, int(info.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return err
	}

	pos := 0

	// skip ID3v2 tag
	if len(a.bytes) >= 10 &&
		a.bytes[0] == 'I' &&
		a.bytes[1] == 'D' &&
		a.bytes[2] == '3' {

		size := int(a.bytes[6]&0x7f)<<21 |
			int(a.bytes[7]&0x7f)<<14 |
			int(a.bytes[8]&0x7f)<<7 |
			int(a.bytes[9]&0x7f)

		pos = 10 + size

		slog.Info("found ID3v2 tag, skipping", "nextPos", pos)
	}

	// index frames
	var sampleOffset int64
	for pos+4 <= len(a.bytes) {
		var header [4]byte
		copy(header[:], a.bytes[pos:pos+4])

		var f frame
		if !f.Parse(header, pos) {
			pos++
			continue
		}

		if pos+f.length > len(a.bytes) {
			break
		}

		f.sampleOffset = sampleOffset
		sampleOffset += int64(f.samples)

		a.frames = append(a.frames, f)
		pos += f.length
	}

	if len(a.frames) == 0 {
		return errors.New("no MP3 frames found")
	}

	slog.Info("audio loaded", "path", mp3File, "frames", len(a.frames))

	return err
}

// TotalSamples returns the length of the track in samples, or 0 if nothing is loaded.
func (a *Audio) TotalSamples() int64 {
	if len(a.frames) == 0 {
		return 0
	}

	f := a.frames[len(a.frames)-1]
	return f.sampleOffset + int64(f.samples)
}

// SampleAt maps a wall clock time to a sample offset in the track, wrapping around the
// track length and always returning a non-negative offset. This is what lets a listener
// who connects partway through still join the track where everyone else is, rather than
// restarting it.
func (a *Audio) SampleAt(t time.Time) int64 {
	totalSamples := a.TotalSamples()
	if totalSamples == 0 {
		return 0
	}

	sampleRate := int64(a.frames[0].sampleRate)
	elapsed := t.Sub(epoch)

	seconds := int64(elapsed / time.Second)
	remainder := elapsed % time.Second

	samples := seconds*sampleRate +
		int64(remainder)*sampleRate/int64(time.Second)

	samples %= totalSamples

	if samples < 0 {
		samples += totalSamples
	}

	return samples
}

// Chunk is one MPEG frame ready to be written to a client. Data aliases the memory
// mapped file and must not be modified. Duration is how long the frame takes to play,
// which the caller uses to pace writes.
type Chunk struct {
	Data     []byte
	Duration time.Duration
}

// StreamFromSample streams frames starting at the frame containing sample, and returns
// an error if no frame covers that sample. Frames are produced as fast as the receiver
// reads them, with no pacing applied; it is the caller's job to sleep for each chunk's
// Duration to play back in real time.
//
// If loop is set the stream wraps around at the end of the file instead of ending. The
// returned channel is closed once the track finishes, or as soon as ctx is cancelled,
// so a receiver that stops reading will not leak the producing goroutine.
//
// onLoop, when non-nil, is called from the producing goroutine each time the stream
// returns to the frame it started on, i.e. once per full pass over the track. Because
// the count is relative to the join point, a listener who starts mid-track is not
// counted until they have heard the entire track.
func (a *Audio) StreamFromSample(ctx context.Context, sample int64, loop bool, onLoop func()) (<-chan Chunk, error) {
	index, ok := slices.BinarySearchFunc(
		a.frames,
		sample,
		func(f frame, sample int64) int {
			if sample < f.sampleOffset {
				return 1
			}
			if sample >= f.sampleOffset+int64(f.samples) {
				return -1
			}
			return 0
		},
	)

	if !ok {
		return nil, errors.New("sample out of bounds")
	}

	// store starting index so loops are counted relative to where the listener joined
	startIndex := index

	slog.Info("starting streaming",
		"requestedSample", sample,
		"startingSample", a.frames[index].sampleOffset,
		"frame", index,
	)

	c := make(chan Chunk, 1)
	go func() {
		defer close(c)

		for {
			f := a.frames[index]

			chunk := Chunk{
				Data: a.bytes[f.offset : f.offset+f.length],
				Duration: time.Duration(
					int64(f.samples) * int64(time.Second) / int64(f.sampleRate),
				),
			}

			select {
			case c <- chunk:
			case <-ctx.Done():
				return
			}

			index++
			if index >= len(a.frames) {
				if !loop {
					return
				}

				index = 0
			}

			// checked after the wrap so startIndex 0 is caught too
			if index == startIndex && onLoop != nil {
				onLoop()
			}
		}
	}()

	return c, nil
}
