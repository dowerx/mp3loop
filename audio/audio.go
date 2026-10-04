package audio

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"
)

var epoch = time.Unix(0, 0)

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

type Audio struct {
	bytes  []byte
	frames []frame
}

func (a *Audio) Load(mp3File string) (err error) {
	a.bytes, err = os.ReadFile(mp3File)
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

	return err
}

func (a *Audio) TotalSamples() int64 {
	if len(a.frames) == 0 {
		return 0
	}

	f := a.frames[len(a.frames)-1]
	return f.sampleOffset + int64(f.samples)
}

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

type Chunk struct {
	Data     []byte
	Duration time.Duration
}

func (a *Audio) StreamFromSample(ctx context.Context, sample int64, loop bool) (<-chan Chunk, error) {
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
		}
	}()

	return c, nil
}
