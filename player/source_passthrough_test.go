package player

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeByteSource is a deterministic AudioSourcePassthrough for exercising
// SwitchingAudioSource.ReadBytes. Bytes are served from a slice in chunks of
// at most maxRead bytes per call.
type fakeByteSource struct {
	mu      sync.Mutex
	data    []byte
	pos     int
	maxRead int
}

func (f *fakeByteSource) ReadBytes(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pos >= len(f.data) {
		return 0, io.EOF
	}

	n := min(len(p), len(f.data)-f.pos)
	if f.maxRead > 0 {
		n = min(n, f.maxRead)
	}

	copy(p, f.data[f.pos:f.pos+n])
	f.pos += n
	return n, nil
}

// Read is never used in passthrough mode; it only satisfies AudioSource.
func (f *fakeByteSource) Read([]float32) (int, error) { return 0, io.EOF }

func (f *fakeByteSource) SetPositionMs(int64) error { return nil }

func (f *fakeByteSource) PositionMs() int64 { return 0 }

// readBytesUntilEOF drains the source's passthrough path with the given read
// chunk size, failing the test if EOF never surfaces.
func readBytesUntilEOF(t *testing.T, s *SwitchingAudioSource, chunk int) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, chunk)
	for i := 0; ; i++ {
		if i > 1_000_000 {
			t.Fatal("readBytesUntilEOF: too many iterations, EOF never surfaced")
		}
		n, err := s.ReadBytes(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		} else if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
	}
}

// TestReadBytesPassthrough verifies that the raw bytes of consecutive sources
// are handed through untouched and concatenated: the primary's EOF switches to
// the prefetched secondary without surfacing, and the final EOF comes through
// once both are drained.
func TestReadBytesPassthrough(t *testing.T) {
	first := []byte(strings.Repeat("A", 1000))
	second := []byte(strings.Repeat("B", 700))

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(&fakeByteSource{data: first, maxRead: 128})
	s.SetSecondary(&fakeByteSource{data: second, maxRead: 256})

	out := readBytesUntilEOF(t, s, 300)
	if !bytes.Equal(out, append(append([]byte{}, first...), second...)) {
		t.Fatalf("passthrough bytes mismatch: got %d bytes, want %d+%d concatenated", len(out), len(first), len(second))
	}
}

// TestReadBytesDoneNonBlocking verifies that the end-of-source signal is
// delivered without blocking: draining both sources to EOF while nobody
// consumes Done() must complete (an earlier implementation did a blocking
// send on the done channel while holding the source lock, deadlocking the
// output loop when the daemon was not ready to receive).
func TestReadBytesDoneNonBlocking(t *testing.T) {
	s := NewSwitchingAudioSource(0)
	s.SetPrimary(&fakeByteSource{data: []byte("first-track")})
	s.SetSecondary(&fakeByteSource{data: []byte("second-track")})

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		buf := make([]byte, 64)
		for i := 0; ; i++ {
			if i > 1_000_000 {
				t.Error("too many iterations, EOF never surfaced")
				return
			}
			if _, err := s.ReadBytes(buf); err == io.EOF {
				break
			} else if err != nil {
				t.Errorf("unexpected read error: %v", err)
				return
			}
		}

		// A second read past EOF must also return without blocking and
		// without a duplicate done signal.
		if _, err := s.ReadBytes(buf); err != io.EOF {
			t.Errorf("expected EOF on read past the end, got %v", err)
		}
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadBytes blocked on the done signal")
	}
}

// TestReadBytesEOFDedup verifies that the done signal fires exactly once per
// track transition and is re-armed by SetPrimary, mirroring what upstream
// fixed for Read: without deduplication the daemon skipped multiple tracks at
// the end of a looped context.
func TestReadBytesEOFDedup(t *testing.T) {
	s := NewSwitchingAudioSource(0)
	s.SetPrimary(&fakeByteSource{data: []byte("first-track")})
	s.SetSecondary(&fakeByteSource{data: []byte("second-track")})

	// Drain everything: the primary-to-secondary switch reports done once;
	// the secondary's final EOF must not report a second time (eofReported
	// is still set until the daemon acknowledges via SetPrimary).
	readBytesUntilEOF(t, s, 64)

	select {
	case <-s.Done():
	default:
		t.Fatal("expected a done signal after the track transition")
	}

	select {
	case <-s.Done():
		t.Fatal("done signal was not deduplicated")
	default:
	}

	// SetPrimary re-arms the signal for the next track.
	s.SetPrimary(&fakeByteSource{data: []byte("third-track")})
	readBytesUntilEOF(t, s, 64)

	select {
	case <-s.Done():
	default:
		t.Fatal("expected a done signal after SetPrimary re-armed it")
	}
}

// TestReadBytesRequiresPassthroughSource verifies that a source without
// ReadBytes support is rejected with an error instead of misbehaving.
func TestReadBytesRequiresPassthroughSource(t *testing.T) {
	s := NewSwitchingAudioSource(0)
	s.SetPrimary(constSource(64, 0.5, 0))

	if _, err := s.ReadBytes(make([]byte, 64)); err == nil {
		t.Fatal("expected an error for a source that does not support passthrough")
	}
}
