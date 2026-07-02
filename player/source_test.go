package player

import (
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"
)

// fakeSource is a deterministic AudioSource for exercising
// SwitchingAudioSource. Samples are served from a slice in chunks of at most
// maxRead samples per call.
type fakeSource struct {
	mu      sync.Mutex
	samples []float32
	pos     int
	maxRead int
	// eofWithLastRead makes the final Read return (n>0, io.EOF) together,
	// mirroring sources that report EOF alongside the last chunk.
	eofWithLastRead bool
	// failAt injects failErr once pos reaches it (non-EOF error paths).
	failAt  int
	failErr error
}

func (f *fakeSource) Read(p []float32) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failErr != nil && f.pos >= f.failAt {
		return 0, f.failErr
	}

	remaining := len(f.samples) - f.pos
	if remaining == 0 {
		return 0, io.EOF
	}

	n := min(len(p), remaining)
	if f.failErr != nil {
		n = min(n, f.failAt-f.pos)
	}
	if f.maxRead > 0 {
		n = min(n, f.maxRead)
	}

	copy(p, f.samples[f.pos:f.pos+n])
	f.pos += n

	if f.eofWithLastRead && f.pos == len(f.samples) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeSource) SetPositionMs(pos int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pos = int(pos) * SampleRate / 1000 * Channels
	return nil
}

func (f *fakeSource) PositionMs() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(f.pos / Channels * 1000 / SampleRate)
}

// constSource builds a fake source of n samples all set to value v.
func constSource(n int, v float32, maxRead int) *fakeSource {
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = v
	}
	return &fakeSource{samples: samples, maxRead: maxRead}
}

// rampSource builds a fake source whose sample i has value base+i, making
// positions recognizable in the output.
func rampSource(n int, base float32, maxRead int) *fakeSource {
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = base + float32(i)
	}
	return &fakeSource{samples: samples, maxRead: maxRead}
}

// readUntilEOF drains the source with the given read chunk size.
func readUntilEOF(t *testing.T, s *SwitchingAudioSource, chunk int) []float32 {
	t.Helper()
	var out []float32
	buf := make([]float32, chunk)
	for i := 0; ; i++ {
		if i > 1_000_000 {
			t.Fatal("readUntilEOF: too many iterations, EOF never surfaced")
		}
		n, err := s.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		} else if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
	}
}

// readUntilFading consumes audio until the crossfade engages.
func readUntilFading(t *testing.T, s *SwitchingAudioSource, chunk int) (consumed int) {
	t.Helper()
	buf := make([]float32, chunk)
	for i := 0; ; i++ {
		if i > 1_000_000 {
			t.Fatal("readUntilFading: fade never engaged")
		}
		s.cond.L.Lock()
		fading := s.fading
		s.cond.L.Unlock()
		if fading {
			return consumed
		}
		n, err := s.Read(buf)
		if err != nil {
			t.Fatalf("unexpected error while waiting for fade: %v", err)
		}
		consumed += n
	}
}

// expectDone asserts the done channel has fired.
func expectDone(t *testing.T, s *SwitchingAudioSource) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("done channel did not fire")
	}
}

func expectNotDone(t *testing.T, s *SwitchingAudioSource) {
	t.Helper()
	select {
	case <-s.Done():
		t.Fatal("done channel fired unexpectedly")
	default:
	}
}

func TestDirectPassthroughDisabled(t *testing.T) {
	a := rampSource(1000, 0, 0)
	b := rampSource(500, 100_000, 0)

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(a)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 128)
	if len(out) != 1500 {
		t.Fatalf("expected 1500 samples, got %d", len(out))
	}
	for i := 0; i < 1000; i++ {
		if out[i] != float32(i) {
			t.Fatalf("sample %d: expected %f, got %f", i, float32(i), out[i])
		}
	}
	for i := 0; i < 500; i++ {
		if out[1000+i] != float32(100_000+i) {
			t.Fatalf("sample %d: expected %f, got %f", 1000+i, float32(100_000+i), out[1000+i])
		}
	}
	expectDone(t, s)
}

func TestDisabledNegativeDurationSafe(t *testing.T) {
	s := NewSwitchingAudioSource(-44100)
	a := rampSource(64, 0, 0)
	s.SetPrimary(a)
	out := readUntilEOF(t, s, 16)
	if len(out) != 64 {
		t.Fatalf("expected 64 samples, got %d", len(out))
	}
}

// TestCrossfadeMixExact verifies the fade region sample-by-sample against the
// equal-power formula and checks total output length.
func TestCrossfadeMixExact(t *testing.T) {
	const fade = 400 // samples
	const lenA, lenB = 2000, 1600
	const va, vb = 0.25, 0.5

	a := constSource(lenA, va, 0)
	b := constSource(lenB, vb, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 128)

	wantLen := lenA + lenB - fade
	if len(out) != wantLen {
		t.Fatalf("expected %d samples, got %d", wantLen, len(out))
	}

	// Region 1: pure A.
	for i := 0; i < lenA-fade; i++ {
		if out[i] != va {
			t.Fatalf("pre-fade sample %d: expected %f, got %f", i, float32(va), out[i])
		}
	}

	// Region 2: the fade, equal-power mix computed per frame.
	for i := 0; i < fade; i++ {
		frameStart := i - i%Channels
		x := float64(frameStart) / float64(fade)
		gainOut := float32(math.Cos(x * math.Pi / 2))
		gainIn := float32(math.Sin(x * math.Pi / 2))
		want := va*gainOut + vb*gainIn

		got := out[lenA-fade+i]
		if diff := float64(got - want); math.Abs(diff) > 1e-6 {
			t.Fatalf("fade sample %d: expected %f, got %f", i, want, got)
		}
	}

	// Region 3: pure B.
	for i := lenA; i < wantLen; i++ {
		if out[i] != vb {
			t.Fatalf("post-fade sample %d: expected %f, got %f", i, float32(vb), out[i])
		}
	}

	expectDone(t, s)
}

// TestCrossfadeClampsPeaks checks that two loud correlated signals, whose
// equal-power sum would exceed full scale mid-fade, are clamped below the
// int16 overflow threshold.
func TestCrossfadeClampsPeaks(t *testing.T) {
	const fade = 200
	a := constSource(1000, 0.9, 0)
	b := constSource(1000, 0.9, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 64)
	var clamped bool
	for i, v := range out {
		if v > maxSampleValue || v < -1 {
			t.Fatalf("sample %d out of range: %f", i, v)
		}
		if v == maxSampleValue {
			clamped = true
		}
	}
	if !clamped {
		t.Fatal("expected the clamp to engage mid-fade (0.9*(cos+sin) peaks above 1)")
	}
}

// TestNoSecondaryBitExact verifies that with crossfade enabled but no next
// track, the lookahead path is byte-for-byte transparent and EOF timing
// matches the original behavior.
func TestNoSecondaryBitExact(t *testing.T) {
	const n = 5000
	a := rampSource(n, 0, 0)

	s := NewSwitchingAudioSource(600)
	s.SetPrimary(a)

	var out []float32
	buf := make([]float32, 128)
	for {
		nn, err := s.Read(buf)
		out = append(out, buf[:nn]...)
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) < n {
			expectNotDone(t, s)
		}
	}

	if len(out) != n {
		t.Fatalf("expected %d samples, got %d", n, len(out))
	}
	for i := range out {
		if out[i] != float32(i) {
			t.Fatalf("sample %d: expected %f, got %f", i, float32(i), out[i])
		}
	}
	expectDone(t, s)
}

// TestLateSecondaryDuringDrain attaches the next track while the tail of the
// previous one is already draining; the remaining tail should crossfade.
func TestLateSecondaryDuringDrain(t *testing.T) {
	const fade = 400
	const lenA = 1000
	a := constSource(lenA, 0.5, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)

	// Drain some of the track. After these reads the decoder has hit EOF and
	// part of the tail is gone.
	buf := make([]float32, 200)
	var consumed int
	for consumed < 800 {
		n, err := s.Read(buf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		consumed += n
	}

	// Now the next track shows up late.
	b := constSource(1000, 0.25, 0)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 200)
	total := consumed + len(out)

	// The remaining tail (lenA - consumed) overlaps with B.
	wantTotal := lenA + 1000 - (lenA - consumed)
	if total != wantTotal {
		t.Fatalf("expected %d total samples, got %d", wantTotal, total)
	}

	// The middle of the shortened fade must contain contributions of both
	// tracks (equal-power correctly starts at pure A, so sample 0 is not
	// mixed).
	// Equal-power mixing can exceed either input mid-fade; assert the sample
	// is simply not equal to either pure value.
	mid := (lenA - consumed) / 2
	if v := out[mid]; math.Abs(float64(v-0.5)) < 0.01 || math.Abs(float64(v-0.25)) < 0.01 {
		t.Fatalf("expected mid-fade sample to be a mix of both tracks, got %f", v)
	}
}

// TestSecondaryShorterThanFade makes the incoming track end before the fade
// completes; the tail must finish against silence without stalling and the
// second track's end must be reported.
func TestSecondaryShorterThanFade(t *testing.T) {
	const fade = 800
	a := constSource(2000, 0.5, 0)
	b := constSource(300, 0.25, 0) // much shorter than the fade

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 128)

	// All of A (with its tail faded) plus all of B mixed in.
	if len(out) != 2000 {
		t.Fatalf("expected 2000 samples, got %d", len(out))
	}
	expectDone(t, s)
}

// TestSkipMidFadeResets replaces the playing source mid-fade (a manual skip);
// the fade must be aborted and the new source play cleanly from its start.
func TestSkipMidFadeResets(t *testing.T) {
	const fade = 400
	a := constSource(1000, 0.5, 0)
	b := constSource(1000, 0.25, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	// Read until the crossfade into B is genuinely in progress.
	readUntilFading(t, s, 100)

	// User skips to a new track entirely.
	c := rampSource(500, 100_000, 0)
	s.SetPrimary(c)

	out := readUntilEOF(t, s, 100)
	if len(out) != 500 {
		t.Fatalf("expected 500 samples of C, got %d", len(out))
	}
	for i := range out {
		if out[i] != float32(100_000+i) {
			t.Fatalf("sample %d: expected pure C value %f, got %f", i, float32(100_000+i), out[i])
		}
	}
}

// TestPromoteSameSourceKeepsFade re-sets the already-promoted source (what
// the daemon does when acknowledging a track change) and verifies the fade
// keeps going instead of restarting.
func TestPromoteSameSourceKeepsFade(t *testing.T) {
	const fade = 400
	a := constSource(1000, 0.5, 0)
	b := constSource(1000, 0.25, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	consumed := readUntilFading(t, s, 100)

	// Daemon acknowledges the transition with the same source pointer.
	s.SetPrimary(b)

	s.cond.L.Lock()
	fading := s.fading
	s.cond.L.Unlock()
	if !fading {
		t.Fatal("re-promoting the same source must not abort the fade")
	}

	out := readUntilEOF(t, s, 100)
	if consumed+len(out) != 1000+1000-fade {
		t.Fatalf("expected %d total samples, got %d", 2000-fade, consumed+len(out))
	}
}

// TestSeekResetsLookahead seeks during normal lookahead playback and checks
// the output resumes from the seek target with no stale buffered audio.
func TestSeekResetsLookahead(t *testing.T) {
	// 2 seconds of ramp audio.
	n := 2 * SampleRate * Channels
	a := rampSource(n, 0, 0)

	s := NewSwitchingAudioSource(800)
	s.SetPrimary(a)

	buf := make([]float32, 256)
	if _, err := s.Read(buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Seek to 1000ms.
	if err := s.SetPositionMs(1000); err != nil {
		t.Fatalf("seek failed: %v", err)
	}

	nn, err := s.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantFirst := float32(1000 * SampleRate / 1000 * Channels)
	if nn == 0 || buf[0] != wantFirst {
		t.Fatalf("expected first sample after seek to be %f, got %f", wantFirst, buf[0])
	}
}

// TestPositionCompensatesLookahead verifies PositionMs reports where the
// listener is, not where the decoder is.
func TestPositionCompensatesLookahead(t *testing.T) {
	n := 2 * SampleRate * Channels
	a := rampSource(n, 0, 0)

	const fade = 44100 // half a second of lookahead
	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)

	// Consume exactly 250ms of audio.
	consumeTarget := SampleRate / 4 * Channels
	buf := make([]float32, 1024)
	var consumed int
	for consumed < consumeTarget {
		nn, err := s.Read(buf[:min(1024, consumeTarget-consumed)])
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		consumed += nn
	}

	got := s.PositionMs()
	if got < 245 || got > 255 {
		t.Fatalf("expected reported position ~250ms, got %dms", got)
	}

	// Sanity check: the decoder itself is far ahead.
	if raw := a.PositionMs(); raw < 700 {
		t.Fatalf("expected decoder position to be ahead (>=700ms), got %dms", raw)
	}
}

// TestPartialAndOddReads exercises small, odd-sized output buffers and
// chunk-limited sources.
func TestPartialAndOddReads(t *testing.T) {
	const fade = 100
	a := constSource(1000, 0.5, 6) // source dribbles 6 samples at a time
	b := constSource(400, 0.25, 10)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 7) // odd-sized destination buffer
	if len(out) != 1000+400-fade {
		t.Fatalf("expected %d samples, got %d", 1000+400-fade, len(out))
	}
}

// TestEOFWithFinalChunk covers sources that return io.EOF together with the
// last samples instead of on a separate call.
func TestEOFWithFinalChunk(t *testing.T) {
	const fade = 200
	a := constSource(900, 0.5, 0)
	a.eofWithLastRead = true
	b := constSource(600, 0.25, 0)
	b.eofWithLastRead = true

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	out := readUntilEOF(t, s, 128)
	if len(out) != 900+600-fade {
		t.Fatalf("expected %d samples, got %d", 900+600-fade, len(out))
	}
}

// TestDoneFiresOncePerTrack verifies done fires exactly once per finished
// track across a crossfaded transition.
func TestDoneFiresOncePerTrack(t *testing.T) {
	const fade = 200
	a := constSource(800, 0.5, 0)
	b := constSource(800, 0.25, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	// Consume until the fade begins (A's EOF discovered).
	readUntilFading(t, s, 100)
	expectDone(t, s) // A's end reported at fade start

	// Daemon acknowledges the promotion, which arms done for track B.
	s.SetPrimary(b)

	expectNotDone(t, s)
	readUntilEOF(t, s, 100)
	expectDone(t, s) // B's end reported at its EOF
}

// TestServesWithoutFullBuffer verifies playback flows as soon as the fade
// reserve is intact, without demanding a completely full lookahead (a slow
// source must not stall the output while audio is available).
func TestServesWithoutFullBuffer(t *testing.T) {
	const fade = 400
	// Source dribbles 50 samples per read; total is far below the ring
	// capacity of fade+lookaheadHeadroom.
	a := rampSource(2000, 0, 50)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)

	// The very first read must return audio once the reserve is covered,
	// even though the ring is nowhere near full.
	buf := make([]float32, 64)
	n, err := s.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("expected immediate audio with partial buffer, got n=%d err=%v", n, err)
	}
	if buf[0] != 0 {
		t.Fatalf("expected first sample 0, got %f", buf[0])
	}

	// And the reserve must genuinely be intact at this point.
	s.cond.L.Lock()
	if s.bufLen < fade {
		t.Fatalf("fade reserve violated: bufLen=%d < %d", s.bufLen, fade)
	}
	s.cond.L.Unlock()
}

// TestNoStallWithGreedySource models real decoders, which fill whatever
// buffer they are handed: the first read after start must not block until the
// entire reserve is decoded.
func TestNoStallWithGreedySource(t *testing.T) {
	const fade = 44100 * 4        // large reserve
	a := rampSource(fade*3, 0, 0) // greedy: no maxRead cap

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)

	buf := make([]float32, 256)
	n, err := s.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("expected audio, got n=%d err=%v", n, err)
	}

	// A greedy decoder given the whole ring would have decoded
	// fade+lookaheadHeadroom samples; bounded fills must stay far below that.
	a.mu.Lock()
	decoded := a.pos
	a.mu.Unlock()
	if decoded >= fade {
		t.Fatalf("first read decoded %d samples, fill is not bounded", decoded)
	}
}

// TestSkipToPrefetchedNoAlias covers skipping straight to the track that was
// prefetched: promoting it must clear the secondary alias, otherwise EOF
// would fade the stream into its own exhausted self.
func TestSkipToPrefetchedNoAlias(t *testing.T) {
	const fade = 200
	a := constSource(800, 0.5, 0)
	b := rampSource(600, 0, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)

	// User skips straight to the prefetched track.
	s.SetPrimary(b)

	out := readUntilEOF(t, s, 128)
	if len(out) != 600 {
		t.Fatalf("expected exactly B's 600 samples, got %d", len(out))
	}
	for i := range out {
		if out[i] != float32(i) {
			t.Fatalf("sample %d: expected %f got %f (aliased replay?)", i, float32(i), out[i])
		}
	}
}

// TestClearSecondary verifies a nil secondary detaches a previously prefetched
// stream (used when the daemon's next-track plan changes).
func TestClearSecondary(t *testing.T) {
	const fade = 200
	a := constSource(800, 0.5, 0)
	b := constSource(600, 0.25, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)
	s.SetSecondary(b)
	s.SetSecondary(nil)

	out := readUntilEOF(t, s, 128)
	if len(out) != 800 {
		t.Fatalf("expected only A's 800 samples, got %d", len(out))
	}
	for _, v := range out {
		if v != 0.5 {
			t.Fatal("secondary audio leaked after being cleared")
		}
	}
}

// TestErrorDeliveredAfterBufferedAudio: a non-EOF source error must not
// discard audio that was already decoded into the lookahead.
func TestErrorDeliveredAfterBufferedAudio(t *testing.T) {
	const fade = 200
	wantErr := errors.New("network exploded")
	a := rampSource(2000, 0, 0)
	a.failAt = 1000
	a.failErr = wantErr

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)

	var out []float32
	buf := make([]float32, 128)
	var got error
	for {
		n, err := s.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			got = err
			break
		}
	}

	if len(out) != 1000 {
		t.Fatalf("expected all 1000 decoded samples before the error, got %d", len(out))
	}
	if !errors.Is(got, wantErr) {
		t.Fatalf("expected injected error, got %v", got)
	}
}

// TestDisabledSeekKeepsEOFSemantics: with crossfade disabled, a seek must NOT
// re-arm the done signal (original behavior only re-armed it in SetPrimary).
func TestDisabledSeekKeepsEOFSemantics(t *testing.T) {
	a := rampSource(400, 0, 0)

	s := NewSwitchingAudioSource(0)
	s.SetPrimary(a)
	readUntilEOF(t, s, 128)
	expectDone(t, s)

	// Seek back into the still-loaded source and drain it again.
	if err := s.SetPositionMs(0); err != nil {
		t.Fatalf("seek failed: %v", err)
	}
	readUntilEOF(t, s, 128)
	expectNotDone(t, s)
}

// TestConcurrentAccess smokes thread-safety with the race detector.
func TestConcurrentAccess(t *testing.T) {
	const fade = 400
	a := constSource(50_000, 0.5, 0)
	b := constSource(50_000, 0.25, 0)

	s := NewSwitchingAudioSource(fade)
	s.SetPrimary(a)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := make([]float32, 512)
		for {
			_, err := s.Read(buf)
			if err == io.EOF {
				return
			} else if err != nil {
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond)
		s.SetSecondary(b)
		time.Sleep(time.Millisecond)
		_ = s.PositionMs()
		_ = s.SetPositionMs(10)
	}()

	wg.Wait()
}
