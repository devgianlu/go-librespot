package player

import (
	"errors"
	"io"
	"math"
	"sync"

	librespot "github.com/devgianlu/go-librespot"
)

// maxSampleValue is the largest sample value that survives the int16 output
// conversion without overflowing (32767/32768). Mixed samples are clamped to
// [-1, maxSampleValue] so a crossfade can never wrap around to a click.
const maxSampleValue = float32(0x7fff) / float32(0x8000)

// lookaheadHeadroom is how many samples beyond the crossfade length are
// buffered. Serving playback only from this headroom keeps a full crossfade
// worth of audio in reserve, so the fade always has its complete length
// available when the end of a track is discovered.
const lookaheadHeadroom = 8 * 1024

// oggFramedPassthrough is implemented by passthrough sources that track Ogg
// page framing on the bytes they serve, allowing the switcher to hand
// playback over to a new source exactly at a page boundary.
type oggFramedPassthrough interface {
	librespot.AudioSourcePassthrough

	// MidPage reports whether the raw stream currently stands inside an Ogg
	// page (including a partially served page header).
	MidPage() bool

	// PageBytesRemaining returns the number of bytes known to remain before
	// the next page boundary; capping a read at it never overshoots the
	// boundary.
	PageBytesRemaining() int
}

type SwitchingAudioSource struct {
	source map[bool]librespot.AudioSource
	which  bool
	cond   *sync.Cond

	done chan struct{}

	eofReported bool

	// Page-aligned passthrough handoff state. When SetPrimary replaces a
	// passthrough primary that stands mid Ogg page, the incoming source is
	// parked here instead of being swapped in synchronously: ReadBytes keeps
	// serving the old source only up to its current page boundary and then
	// promotes the parked one. Swapping mid-page spliced the new track's BOS
	// and header pages into the middle of the old, now truncated page
	// downstream (field trace 2026-08-01). drainBudget bounds the drain to at
	// most one maximum-size Ogg page, so a source with broken framing can
	// never stall the handoff.
	pendingPrimary librespot.AudioSource
	pendingSet     bool
	drainBudget    int

	// Crossfade state. When crossfadeSamples is zero the source behaves
	// exactly as it did before crossfade support was introduced.
	//
	// While crossfading is enabled, audio is buffered ahead of playback in a
	// ring buffer (buf) holding up to crossfadeSamples plus a small headroom.
	// This lookahead is how the end of the primary source is discovered
	// before it is audible: when the decoder returns EOF, the ring holds the
	// track's tail. If a secondary source is available, the tail is faded out
	// while the secondary fades in with equal-power gains.
	crossfadeSamples int
	buf              []float32 // lookahead ring, len == crossfadeSamples+lookaheadHeadroom
	bufStart         int       // ring read index
	bufLen           int       // samples currently buffered
	primaryEOF       bool      // primary decoder returned EOF, ring holds its tail
	pendingErr       error     // non-EOF read error, delivered once the ring drains
	fading           bool      // mixing ring tail (fade out) with primary slot (fade in)
	fadeTotal        int       // samples of tail at fade start, gain ramp length
	fadePos          int       // samples of tail already mixed
	scratch          []float32 // reusable buffer for fill and fade-in reads
}

// NewSwitchingAudioSource creates an audio source that switches seamlessly
// between a primary and a prefetched secondary source.
//
// If crossfadeSamples is greater than zero, track transitions overlap by up
// to that many samples (interleaved, so frames times channels) with an
// equal-power crossfade. A value of zero disables crossfading and preserves
// gapless behavior.
func NewSwitchingAudioSource(crossfadeSamples int) *SwitchingAudioSource {
	if crossfadeSamples < 0 {
		crossfadeSamples = 0
	}

	// Round down to a whole frame so channels never desynchronize.
	crossfadeSamples -= crossfadeSamples % Channels

	s := &SwitchingAudioSource{
		source:           map[bool]librespot.AudioSource{},
		cond:             sync.NewCond(&sync.Mutex{}),
		done:             make(chan struct{}, 1),
		crossfadeSamples: crossfadeSamples,
	}

	if crossfadeSamples > 0 {
		s.buf = make([]float32, crossfadeSamples+lookaheadHeadroom)
		s.scratch = make([]float32, 8*1024)
	}

	return s
}

func (s *SwitchingAudioSource) SetPrimary(source librespot.AudioSource) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	// After a crossfade begins, the incoming source has already been promoted
	// to the primary slot. The daemon acknowledges the very same transition
	// by re-setting the source it was told is now playing: that must not
	// disturb the fade that is still in progress.
	if s.source[s.which] != source {
		// A passthrough primary standing mid Ogg page must not be swapped
		// out synchronously: the downstream consumer would read the new
		// track's first pages as the tail of the old truncated page. Park the
		// incoming source; ReadBytes drains the old one to its current page
		// boundary and then promotes.
		if fp, ok := s.source[s.which].(oggFramedPassthrough); ok && fp.MidPage() {
			s.pendingPrimary = source
			s.pendingSet = true
			s.drainBudget = maxOggPageSize
			s.eofReported = false
			s.cond.Broadcast()
			return
		}

		// A genuinely new source replaces whatever was there, including any
		// in-flight crossfade tail.
		s.resetCrossfade()
		s.source[s.which] = source
	}

	// Reaching this point means the swap happened synchronously (or the
	// current source was re-asserted): any previously parked handoff is
	// superseded.
	s.pendingPrimary = nil
	s.pendingSet = false

	// If the same source was also prefetched into the secondary slot (e.g. a
	// manual skip straight to the prefetched track), drop the alias: switching
	// a source into itself would replay or fade into an exhausted stream.
	if s.source[!s.which] == source {
		delete(s.source, !s.which)
	}

	s.eofReported = false
	s.cond.Broadcast()
}

func (s *SwitchingAudioSource) SetSecondary(source librespot.AudioSource) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	s.source[!s.which] = source
	s.cond.Broadcast()
}

func (s *SwitchingAudioSource) Done() <-chan struct{} {
	return s.done
}

func (s *SwitchingAudioSource) Read(p []float32) (n int, err error) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	if s.crossfadeSamples == 0 {
		return s.readDirect(p)
	}

	return s.readCrossfade(p)
}

// readDirect is the original, crossfade-free read path.
func (s *SwitchingAudioSource) readDirect(p []float32) (n int, err error) {
	for s.source[s.which] == nil {
		s.cond.Wait()
	}

	n, err = s.source[s.which].Read(p)
	if errors.Is(err, io.EOF) {
		// notify this source is done
		s.reportDone()

		// if there's no other source just let the EOF through
		if s.source[!s.which] == nil {
			return n, err
		}

		// delete current source and switch to the other one
		delete(s.source, s.which)
		s.which = !s.which

		// ignore the EOF, we have more data
		return n, nil
	} else if err != nil {
		return n, err
	}

	return n, nil
}

// ReadBytes mirrors readDirect for passthrough mode: it hands out the current
// source's raw encoded bytes and, on EOF, switches to the queued source. The
// raw streams are concatenated only at Ogg page boundaries: the natural EOF
// chain is page-aligned by construction (a track ends after its last page),
// and a SetPrimary that arrives mid-page is parked and promoted by
// drainToPageBoundary once the current page is fully served. The result is a
// chained bitstream the downstream decoder plays continuously.
//
// Crossfading is impossible on the raw encoded stream (mixing requires
// decoded samples), so ReadBytes always behaves like the crossfade-free
// path; the player disables crossfade when passthrough is enabled.
func (s *SwitchingAudioSource) ReadBytes(p []byte) (n int, err error) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	for {
		for s.source[s.which] == nil {
			s.cond.Wait()
		}

		src, ok := s.source[s.which].(librespot.AudioSourcePassthrough)
		if !ok {
			return 0, errors.New("current source does not support passthrough")
		}

		if s.pendingSet {
			if n, served := s.drainToPageBoundary(src, p); served {
				return n, nil
			}
			// The handoff completed without producing bytes: loop to serve
			// the promoted source so the caller never sees a zero-byte read.
			continue
		}

		n, err = src.ReadBytes(p)
		if errors.Is(err, io.EOF) {
			// notify this source is done (non-blocking, deduplicated)
			s.reportDone()

			// if there's no other source just let the EOF through
			if s.source[!s.which] == nil {
				return n, err
			}

			// delete current source and switch to the other one
			delete(s.source, s.which)
			s.which = !s.which

			// ignore the EOF, we have more data
			return n, nil
		} else if err != nil {
			return n, err
		}

		return n, nil
	}
}

// drainToPageBoundary serves bytes from the outgoing passthrough primary
// while a new primary is parked in pendingPrimary. Reads are capped so they
// can never start a new page; once the current page boundary is reached, the
// bounded drain budget is exhausted, or the outgoing source ends or fails,
// the parked source is promoted. served is false when the handoff completed
// without producing bytes, in which case the caller reads from the promoted
// source instead. Must be called with s.cond.L held.
func (s *SwitchingAudioSource) drainToPageBoundary(src librespot.AudioSourcePassthrough, p []byte) (n int, served bool) {
	fp, ok := src.(oggFramedPassthrough)
	if !ok || !fp.MidPage() || s.drainBudget <= 0 {
		// Already at a boundary, or the source cannot report framing (it
		// should not have been parked then), or the bounded drain is used up:
		// hand over immediately.
		s.promotePending()
		return 0, false
	}

	limit := min(len(p), s.drainBudget)
	if rem := fp.PageBytesRemaining(); rem > 0 {
		limit = min(limit, rem)
	}

	n, err := src.ReadBytes(p[:limit])
	s.drainBudget -= n

	if n == 0 || err != nil || !fp.MidPage() || s.drainBudget <= 0 {
		// The page boundary was reached, or the outgoing source stalled,
		// ended, or failed mid-drain: promote now. Errors (including EOF)
		// from a source that is being replaced anyway are deliberately
		// dropped, and no done signal is sent: the daemon initiated this
		// transition itself, reporting done would make it skip once more.
		s.promotePending()
	}

	return n, n > 0
}

// promotePending replaces the current primary with the parked pending source,
// dropping the outgoing source exactly like a synchronous SetPrimary would
// have. Must be called with s.cond.L held.
func (s *SwitchingAudioSource) promotePending() {
	source := s.pendingPrimary
	s.pendingPrimary = nil
	s.pendingSet = false
	s.drainBudget = 0

	s.source[s.which] = source

	// Drop a prefetched alias of the promoted source, mirroring SetPrimary:
	// switching a source into itself would replay an exhausted stream.
	if s.source[!s.which] == source {
		delete(s.source, !s.which)
	}
}

// readCrossfade is the read path used when crossfading is enabled. Playback
// is served from a lookahead ring buffer so the end of the current track is
// known before it is audible. While the track is still decoding, a full
// crossfade worth of samples is kept in reserve and playback is served only
// from the headroom above it, guaranteeing the complete fade length is
// available the moment EOF is discovered.
func (s *SwitchingAudioSource) readCrossfade(p []float32) (n int, err error) {
	for {
		if s.fading {
			return s.readFade(p)
		}

		for s.source[s.which] == nil && !(s.primaryEOF && s.bufLen > 0) {
			s.cond.Wait()
		}

		// Refill the lookahead with bounded reads. Decoders greedily fill
		// whatever buffer they are handed, so reads are capped to keep each
		// one short; playback is served as soon as anything is buffered and
		// the ring keeps filling a little further on every call. Blocking
		// here only happens when the ring is empty, matching the original
		// path's behavior of blocking on a single source read.
		budget := 2 * len(s.scratch)
		for !s.primaryEOF && s.pendingErr == nil && s.bufLen < len(s.buf) &&
			(s.bufLen == 0 || budget > 0) {
			free := s.freeChunk()
			free = free[:min(len(free), len(s.scratch))]
			nn, err := s.source[s.which].Read(free)
			s.bufLen += nn
			budget -= nn
			if errors.Is(err, io.EOF) {
				// Frame-align defensively so a torn final frame cannot
				// desynchronize channels during the fade.
				s.bufLen -= s.bufLen % Channels
				s.primaryEOF = true
			} else if err != nil {
				// Deliver buffered audio first; the error surfaces once the
				// ring drains, mirroring how the original path returned data
				// alongside errors.
				s.pendingErr = err
			}
		}

		if !s.primaryEOF && s.pendingErr == nil {
			// While the track is still decoding, serve only the audio above
			// the crossfade reserve. If the ring has not built up that far
			// yet (right after a start or seek), serve from what exists so
			// playback is never stalled; a track that ends this early simply
			// gets a proportionally shorter fade.
			limit := s.bufLen - s.crossfadeSamples
			if limit <= 0 {
				limit = min(s.bufLen, len(p))
			}
			return s.popChunk(p, limit), nil
		}

		if s.pendingErr != nil && !s.primaryEOF {
			if s.bufLen > 0 {
				return s.popChunk(p, s.bufLen), nil
			}
			err = s.pendingErr
			s.pendingErr = nil
			return 0, err
		}

		if s.source[!s.which] != nil {
			// There is a next track. Any buffered audio beyond the fade
			// length plays out plainly first.
			if s.bufLen > s.crossfadeSamples {
				return s.popChunk(p, s.bufLen-s.crossfadeSamples), nil
			}

			// Report the current track as done and promote the next one to
			// the primary slot. The buffered tail fades out over it from the
			// very next read.
			s.reportDone()
			delete(s.source, s.which)
			s.which = !s.which
			s.fading = true
			s.fadeTotal = s.bufLen
			s.fadePos = 0
			continue
		}

		if s.bufLen == 0 {
			// Tail fully drained and nobody to switch to: this is the end
			// of playback, matching the original EOF timing.
			s.reportDone()
			return 0, io.EOF
		}

		// No next track (yet): keep draining the tail. A late prefetch can
		// still upgrade this to a crossfade on a following read.
		n = s.popChunk(p, s.bufLen)
		if s.bufLen == 0 {
			s.reportDone()
			return n, io.EOF
		}
		return n, nil
	}
}

// readFade mixes the buffered tail of the previous track (fading out) with
// the already-promoted primary source (fading in) using equal-power gains.
func (s *SwitchingAudioSource) readFade(p []float32) (n int, err error) {
	want := min(len(p), s.fadeTotal-s.fadePos, len(s.scratch))
	want -= want % Channels
	if want <= 0 {
		// Nothing left to mix (e.g. a zero-length tail).
		s.finishFade()
		return 0, nil
	}

	// Read the fade-in side. A short read simply mixes a shorter chunk; EOF
	// means the next track is shorter than the fade, mix against silence and
	// let the regular EOF handling deal with it once the tail is done.
	var nIn int
	if s.source[s.which] != nil {
		nIn, err = s.source[s.which].Read(s.scratch[:want])
		nIn -= nIn % Channels
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}

	chunk := want
	if nIn > 0 {
		chunk = nIn
	}

	for i := 0; i < chunk; i += Channels {
		// One gain per frame, cheap equal-power curves.
		x := float64(s.fadePos+i) / float64(s.fadeTotal)
		gainOut := float32(math.Cos(x * math.Pi / 2))
		gainIn := float32(math.Sin(x * math.Pi / 2))

		for c := 0; c < Channels; c++ {
			sample := s.buf[(s.bufStart+i+c)%len(s.buf)] * gainOut
			if i+c < nIn {
				sample += s.scratch[i+c] * gainIn
			}

			// Clamp so the int16 output conversion cannot overflow.
			if sample > maxSampleValue {
				sample = maxSampleValue
			} else if sample < -1 {
				sample = -1
			}

			p[i+c] = sample
		}
	}

	s.bufStart = (s.bufStart + chunk) % len(s.buf)
	s.bufLen -= chunk
	s.fadePos += chunk

	if s.fadePos >= s.fadeTotal {
		s.finishFade()
	}

	return chunk, nil
}

// finishFade clears all crossfade state, returning to normal lookahead
// buffering of the (already promoted) primary source.
func (s *SwitchingAudioSource) finishFade() {
	s.fading = false
	s.fadeTotal = 0
	s.fadePos = 0
	s.bufStart = 0
	s.bufLen = 0
	s.primaryEOF = false
}

// resetCrossfade drops any lookahead and fade state, for when the current
// source is being replaced entirely (e.g. a manual skip).
func (s *SwitchingAudioSource) resetCrossfade() {
	if s.crossfadeSamples == 0 {
		return
	}

	s.pendingErr = nil
	s.finishFade()
}

// freeChunk returns the largest contiguous writable slice of the ring buffer.
func (s *SwitchingAudioSource) freeChunk() []float32 {
	start := (s.bufStart + s.bufLen) % len(s.buf)
	end := min(start+(len(s.buf)-s.bufLen), len(s.buf))
	return s.buf[start:end]
}

// popChunk copies up to limit buffered samples into p and consumes them from
// the ring. The amount served is frame-aligned so the ring can never end up
// on a torn frame.
func (s *SwitchingAudioSource) popChunk(p []float32, limit int) (n int) {
	want := min(len(p), limit, s.bufLen)
	want -= want % Channels

	for n < want {
		end := min(s.bufStart+(want-n), len(s.buf)-s.bufStart+s.bufStart, s.bufStart+s.bufLen)
		end = min(end, len(s.buf))
		nn := copy(p[n:want], s.buf[s.bufStart:end])
		s.bufStart = (s.bufStart + nn) % len(s.buf)
		s.bufLen -= nn
		n += nn
	}
	return n
}

func (s *SwitchingAudioSource) reportDone() {
	if s.eofReported {
		return
	}

	s.eofReported = true

	select {
	case s.done <- struct{}{}:
	default:
	}
}

func (s *SwitchingAudioSource) SetPositionMs(pos int64) error {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	// While a page-aligned handoff is parked, the daemon is addressing the
	// track it just set as primary, not the outgoing source whose final page
	// is still draining.
	if s.pendingSet && s.pendingPrimary != nil {
		return s.pendingPrimary.SetPositionMs(pos)
	}

	if s.source[s.which] == nil {
		return nil
	}

	// Seeking invalidates both the lookahead and any fade in progress: the
	// remains of the previous track are dropped and buffering restarts at
	// the new position.
	s.resetCrossfade()

	if err := s.source[s.which].SetPositionMs(pos); err != nil {
		return err
	}

	if s.crossfadeSamples > 0 {
		// The crossfade path can discover EOF and report done well before the
		// audio finishes; a seek revives the source, so re-arm the signal.
		// The disabled path keeps the original behavior untouched.
		s.eofReported = false
	}
	return nil
}

func (s *SwitchingAudioSource) PositionMs() int64 {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	// See SetPositionMs: while a handoff is parked, report the position of
	// the track the daemon believes is current.
	if s.pendingSet && s.pendingPrimary != nil {
		return s.pendingPrimary.PositionMs()
	}

	if s.source[s.which] == nil {
		return 0
	}

	pos := s.source[s.which].PositionMs()
	if s.crossfadeSamples > 0 && !s.fading {
		// The decoder runs ahead of playback by the buffered lookahead,
		// report where the listener actually is. While fading, the primary
		// slot holds the incoming track which has no lookahead yet.
		pos -= int64(s.bufLen / Channels * 1000 / SampleRate)
		if pos < 0 {
			pos = 0
		}
	}

	return pos
}

func (s *SwitchingAudioSource) Close() error {
	var err error
	if source, ok := s.source[true].(io.Closer); ok && source != nil {
		err = errors.Join(err, source.Close())
	}
	if source, ok := s.source[false].(io.Closer); ok && source != nil {
		err = errors.Join(err, source.Close())
	}
	if source, ok := s.pendingPrimary.(io.Closer); ok && source != nil {
		err = errors.Join(err, source.Close())
	}
	return err
}
