package player

import (
	"errors"
	"io"
	"sync"
)

// passthroughSource hands out a track's raw Ogg/Vorbis bytes for the
// pipe_passthrough backend, bypassing the Vorbis decoder. The decrypted
// Spotify stream is a complete Ogg bitstream starting at offset 0, so it is
// written through untouched. Position is approximated from bytes consumed;
// seeking is limited to a restart because a mid-page byte seek would corrupt
// the Ogg stream.
type passthroughSource struct {
	r          *io.SectionReader
	size       int64
	durationMs int64

	mu  sync.Mutex
	pos int64 // bytes read so far
}

func newPassthroughSource(r io.ReaderAt, size, durationMs int64) *passthroughSource {
	return &passthroughSource{
		r:          io.NewSectionReader(r, 0, size),
		size:       size,
		durationMs: durationMs,
	}
}

func (p *passthroughSource) ReadBytes(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, err := p.r.Read(b)
	p.pos += int64(n)
	return n, err
}

// Read is never used in passthrough mode; it only satisfies AudioSource.
func (p *passthroughSource) Read([]float32) (int, error) { return 0, io.EOF }

func (p *passthroughSource) PositionMs() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.size <= 0 {
		return 0
	}
	pos := p.pos * p.durationMs / p.size
	if pos > p.durationMs {
		pos = p.durationMs
	}
	return pos
}

// ErrPassthroughCannotSeek is returned for a genuine mid-stream seek request
// on a passthrough source. Callers that merely re-assert the current position
// (the daemon's seek-before-play) can treat it as non-fatal; see
// AppPlayer.play.
var ErrPassthroughCannotSeek = errors.New("passthrough source cannot seek mid-stream")

// seekToleranceMs is how far a requested position may differ from the current
// one and still count as "already there". The daemon re-asserts the state's
// position before every play, and that state position advances with the wall
// clock, so the request routinely arrives a few milliseconds past the
// source's actual position even though nothing needs to move.
const seekToleranceMs = 1000

func (p *passthroughSource) SetPositionMs(posMs int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Only a restart is safe; a mid-stream byte seek would split an Ogg page.
	if posMs <= 0 {
		_, err := p.r.Seek(0, io.SeekStart)
		p.pos = 0
		return err
	}
	// A seek that lands (within tolerance) on the position we are already at
	// is a no-op, not a failure: it does not ask the stream to move, it only
	// re-asserts where playback already is. The daemon does exactly this
	// before every play. Refusing it failed the whole play() call, so a
	// passthrough track loaded with a stored position of a few milliseconds
	// never started playing and the player advanced past it: one skip
	// audibly jumped two tracks (field trace 2026-08-01, "cannot seek to
	// 1ms" followed by zero audio pages).
	var curMs int64
	if p.size > 0 {
		curMs = p.pos * p.durationMs / p.size
	}
	if d := posMs - curMs; d >= -seekToleranceMs && d <= seekToleranceMs {
		return nil
	}
	// Report a genuine jump as failed instead of pretending it happened:
	// returning nil made the daemon acknowledge a position jump the audio
	// never performed, so the reported position stayed wrong until the next
	// track. With a real error the controller (e.g. the Spotify app) snaps
	// back to the actual position.
	return ErrPassthroughCannotSeek
}

func (p *passthroughSource) Close() error { return nil }
