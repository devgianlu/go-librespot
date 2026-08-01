package player

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// newTestPassthrough builds a passthrough source over sz bytes representing
// durMs of audio, with already consumed bytes so PositionMs reports a
// mid-stream position.
func newTestPassthrough(t *testing.T, sz, durMs, consumed int64) *passthroughSource {
	t.Helper()
	p := newPassthroughSource(bytes.NewReader(make([]byte, sz)), sz, durMs)
	if consumed > 0 {
		if _, err := io.CopyN(io.Discard, readerFrom(p), consumed); err != nil {
			t.Fatalf("consuming %d bytes: %v", consumed, err)
		}
	}
	return p
}

// readerFrom adapts ReadBytes to io.Reader for the test helper.
func readerFrom(p *passthroughSource) io.Reader { return passthroughReader{p} }

type passthroughReader struct{ p *passthroughSource }

func (r passthroughReader) Read(b []byte) (int, error) { return r.p.ReadBytes(b) }

// The daemon re-asserts the state's position before every play; that state
// advances with the wall clock, so the request routinely lands a few
// milliseconds past the source's actual position. It must be a no-op, not a
// failure: refusing it failed play() outright, the track never started, and
// the player moved past it (one Next press audibly skipped two tracks).
func TestPassthroughSeekToCurrentPositionIsNoOp(t *testing.T) {
	// Fresh stream at position 0: the classic "seek to 1ms" from a stored
	// resume position.
	p := newTestPassthrough(t, 4_000_000, 200_000, 0)
	if err := p.SetPositionMs(1); err != nil {
		t.Fatalf("seek to 1ms on a fresh stream must be a no-op, got %v", err)
	}
	if got := p.PositionMs(); got != 0 {
		t.Fatalf("position after no-op seek = %dms, want 0", got)
	}

	// Mid-stream, re-asserting the current position (resume after pause).
	p = newTestPassthrough(t, 4_000_000, 200_000, 2_000_000) // ~100s in
	cur := p.PositionMs()
	if err := p.SetPositionMs(cur + 40); err != nil {
		t.Fatalf("re-asserting the current position must succeed, got %v", err)
	}
}

// A genuine jump still fails loudly (the controller must snap back to the
// real position instead of believing a jump the audio never performed), and
// the error is the exported sentinel so callers can tell it apart.
func TestPassthroughSeekMidStreamJumpFails(t *testing.T) {
	p := newTestPassthrough(t, 4_000_000, 200_000, 0)
	err := p.SetPositionMs(60_000)
	if err == nil {
		t.Fatal("mid-stream jump must fail")
	}
	if !errors.Is(err, ErrPassthroughCannotSeek) {
		t.Fatalf("error = %v, want ErrPassthroughCannotSeek", err)
	}
}

// Seeking to <= 0 rewinds to the start (the documented restart escape hatch).
func TestPassthroughSeekZeroRestarts(t *testing.T) {
	p := newTestPassthrough(t, 4_000_000, 200_000, 1_000_000)
	if err := p.SetPositionMs(0); err != nil {
		t.Fatalf("restart seek: %v", err)
	}
	if got := p.PositionMs(); got != 0 {
		t.Fatalf("position after restart = %dms, want 0", got)
	}
}
