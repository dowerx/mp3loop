package audio

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func synthAudio(n int) *Audio {
	a := &Audio{bytes: make([]byte, n*4)}
	for i := 0; i < n; i++ {
		a.frames = append(a.frames, frame{
			offset:       i * 4,
			length:       4,
			sampleOffset: int64(i) * 576,
			samples:      576,
			sampleRate:   44100,
		})
	}
	return a
}

// eventually reports whether cond became true within a short deadline.
func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}

	return cond()
}

func TestLoopsCountedForEveryStartIndex(t *testing.T) {
	const frames = 6

	for start := 0; start < frames; start++ {
		a := synthAudio(frames)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var loops atomic.Int64

		ch, err := a.StreamFromSample(
			ctx,
			int64(start)*576+10, // sample inside frame `start`
			true,
			func() { loops.Add(1) },
		)
		if err != nil {
			t.Fatalf("start %d: unexpected error: %v", start, err)
		}

		// A loop is one full pass over the track, which is always `frames` chunks
		// regardless of where the listener joined.
		want := 3
		total := want * frames

		for i := 0; i < total; i++ {
			select {
			case _, ok := <-ch:
				if !ok {
					t.Fatalf("start %d: stream closed early after %d chunks", start, i)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("start %d: timed out after %d chunks", start, i)
			}
		}

		// the final count happens just after the last chunk is handed off, so give
		// the producer a moment to record it before asserting
		if !eventually(t, func() bool { return loops.Load() == int64(want) }) {
			t.Errorf("start %d: got %d loops after %d passes, want %d", start, loops.Load(), want, want)
		}
	}
}

// TestLoopsAreRelativeToStartIndex pins the semantics: the counter tracks returns to
// the frame the listener joined on, not crossings of the end of the file. A listener
// who joins mid-track needs only one frame to reach the end, but must still traverse
// the whole track before the first loop is counted.
func TestLoopsAreRelativeToStartIndex(t *testing.T) {
	a := synthAudio(6)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var loops atomic.Int64

	// start at frame 5, the final frame
	ch, err := a.StreamFromSample(ctx, 5*576+10, true, func() { loops.Add(1) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// one frame reaches the end of the file; counting end-of-file crossings would
	// fire here, but a full pass over the track has not happened yet
	if _, ok := <-ch; !ok {
		t.Fatal("stream closed early")
	}
	if got := loops.Load(); got != 0 {
		t.Errorf("got %d loops after reaching end of file, want 0", got)
	}

	// the remaining 5 frames complete the first full pass
	for i := 0; i < 5; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("stream closed early after %d extra chunks", i)
		}
	}
	if !eventually(t, func() bool { return loops.Load() == 1 }) {
		t.Errorf("got %d loops after one full pass, want 1", loops.Load())
	}

	// a second full pass increments again
	for i := 0; i < 6; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("stream closed early after %d extra chunks", i)
		}
	}
	if !eventually(t, func() bool { return loops.Load() == 2 }) {
		t.Errorf("got %d loops after two full passes, want 2", loops.Load())
	}
}

func TestNoLoopDoesNotCount(t *testing.T) {
	a := synthAudio(6)

	var loops atomic.Int64

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := a.StreamFromSample(ctx, 2*576+10, false, func() { loops.Add(1) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n := 0
	for range ch {
		n++
	}

	if want := 6 - 2; n != want {
		t.Errorf("consumed %d chunks, want %d", n, want)
	}
	if got := loops.Load(); got != 0 {
		t.Errorf("got %d loops, want 0", got)
	}
}

func TestNilOnLoopIsSafe(t *testing.T) {
	a := synthAudio(4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := a.StreamFromSample(ctx, 0, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n := 0
	for range ch {
		n++
		if n > 4*3 {
			break
		}
	}
}

func TestSampleOutOfBounds(t *testing.T) {
	a := synthAudio(6)

	if _, err := a.StreamFromSample(context.Background(), 6*576, true, nil); err == nil {
		t.Error("expected an error for an out of bounds sample, got nil")
	}
}
