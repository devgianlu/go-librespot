package player

import (
	"io"
	"sync"
)

// passthroughSource hands out a track's raw Ogg/Vorbis bytes for the pipe
// backend's passthrough mode, bypassing the Vorbis decoder. The decrypted
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

func (p *passthroughSource) SetPositionMs(posMs int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Only a restart is safe; a mid-stream byte seek would split an Ogg page.
	if posMs <= 0 {
		_, err := p.r.Seek(0, io.SeekStart)
		p.pos = 0
		return err
	}
	return nil
}

func (p *passthroughSource) Close() error { return nil }
