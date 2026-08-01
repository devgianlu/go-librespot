package player

import (
	"errors"
	"io"
	"sync"
)

// Ogg page framing constants.
const (
	// oggHeaderFixedSize is the fixed part of an Ogg page header: capture
	// pattern through the segment-count byte. The segment table of up to 255
	// lacing values follows.
	oggHeaderFixedSize = 27

	// oggCapturePattern starts every Ogg page.
	oggCapturePattern = "OggS"

	// maxOggPageSize is the largest possible Ogg page: 27 fixed header
	// bytes, 255 segment-table bytes, and 255*255 payload bytes.
	maxOggPageSize = oggHeaderFixedSize + 255 + 255*255 // 65307
)

// passthroughSource hands out a track's raw Ogg/Vorbis bytes for the
// pipe_passthrough backend, bypassing the Vorbis decoder. The decrypted
// Spotify stream is a complete Ogg bitstream starting at offset 0, so it is
// written through untouched. Position is approximated from bytes consumed;
// seeking is limited to a restart because a mid-page byte seek would corrupt
// the Ogg stream.
//
// The source additionally tracks Ogg page framing on the bytes it serves, so
// the switching source can hand playback over to another track exactly at a
// page boundary instead of splicing a new stream into a half-written page.
type passthroughSource struct {
	r          *io.SectionReader
	size       int64
	durationMs int64

	mu  sync.Mutex
	pos int64 // bytes read so far

	// Page framing state. pageLeft counts the bytes of the current page not
	// yet served once the page's header has been parsed. At a boundary, hdr
	// accumulates header bytes (the header itself may straddle multiple
	// ReadBytes calls) until the page's total length is known. framingLost is
	// set when the capture pattern does not match; tracking then stops and
	// MidPage reports false, degrading handoffs to the old immediate-swap
	// behavior instead of stalling them forever.
	pageLeft    int
	hdr         []byte
	framingLost bool
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
	p.trackFraming(b[:n])
	return n, err
}

// trackFraming advances the Ogg page framing state over a chunk of bytes that
// was just served. It is cheap for arbitrary read sizes: inside a page body it
// only decrements a counter; header parsing touches at most 27+255 bytes per
// page.
func (p *passthroughSource) trackFraming(b []byte) {
	if p.framingLost {
		return
	}

	for len(b) > 0 {
		if p.pageLeft > 0 {
			// Inside the current page's payload.
			n := min(p.pageLeft, len(b))
			p.pageLeft -= n
			b = b[n:]
			continue
		}

		// At a page boundary: accumulate header bytes until the page's total
		// length is known. Byte 26 holds the segment count; the segment table
		// of that many lacing values follows, and their sum is the payload
		// length.
		need := oggHeaderFixedSize - len(p.hdr)
		if len(p.hdr) >= oggHeaderFixedSize {
			need = oggHeaderFixedSize + int(p.hdr[26]) - len(p.hdr)
		}
		n := min(need, len(b))
		p.hdr = append(p.hdr, b[:n]...)
		b = b[n:]

		// Validate as much of the capture pattern as has arrived. Losing sync
		// means the byte counts cannot be trusted, so stop tracking rather
		// than mis-frame.
		if lim := min(len(p.hdr), len(oggCapturePattern)); string(p.hdr[:lim]) != oggCapturePattern[:lim] {
			p.framingLost = true
			p.hdr = nil
			return
		}

		if len(p.hdr) < oggHeaderFixedSize {
			continue // rest of the fixed header not served yet
		}
		segs := int(p.hdr[26])
		if len(p.hdr) < oggHeaderFixedSize+segs {
			continue // rest of the segment table not served yet
		}

		body := 0
		for _, v := range p.hdr[oggHeaderFixedSize:] {
			body += int(v)
		}
		p.pageLeft = body
		p.hdr = p.hdr[:0]
	}
}

// MidPage reports whether the raw stream currently stands inside an Ogg page,
// including a partially served page header. It is false exactly at page
// boundaries (and when framing was lost, so a handoff never stalls).
func (p *passthroughSource) MidPage() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.framingLost && (p.pageLeft > 0 || len(p.hdr) > 0)
}

// PageBytesRemaining returns the number of bytes known to remain before the
// next page boundary: the rest of the current page's payload, or, while the
// header is still being served, the bytes needed to complete the header.
// Capping reads at this value can therefore never overshoot a boundary, even
// though mid-header the page's full length is not yet known.
func (p *passthroughSource) PageBytesRemaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.framingLost {
		return 0
	}
	if p.pageLeft > 0 {
		return p.pageLeft
	}
	if len(p.hdr) == 0 {
		return 0
	}
	if len(p.hdr) >= oggHeaderFixedSize {
		return oggHeaderFixedSize + int(p.hdr[26]) - len(p.hdr)
	}
	return oggHeaderFixedSize - len(p.hdr)
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
		// The stream starts over at a page boundary: reset the framing state.
		p.pageLeft = 0
		p.hdr = nil
		p.framingLost = false
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
