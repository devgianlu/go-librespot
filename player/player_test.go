package player

import (
	"slices"
	"sync"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/output"
)

// recordingOutput is a fake output.Output that records call order. Drop also
// snapshots which source was primary when it ran, so tests can prove Drop
// executes before source.SetPrimary swaps in the new track.
type recordingOutput struct {
	mu    sync.Mutex
	calls []string

	// source is the SwitchingAudioSource manageLoop passed as the reader when
	// creating this output; captured by the newOutput hook.
	source *SwitchingAudioSource

	// dropPrimary records, once per Drop() call, the primary source observed
	// right before the flush.
	dropPrimary []librespot.AudioSource

	// devices records, in order, the device names manageLoop opened the output
	// on (captured by the newOutput hook).
	devices []string
}

func (o *recordingOutput) record(name string) {
	o.mu.Lock()
	o.calls = append(o.calls, name)
	o.mu.Unlock()
}

func (o *recordingOutput) Pause() error  { o.record("Pause"); return nil }
func (o *recordingOutput) Resume() error { o.record("Resume"); return nil }

func (o *recordingOutput) Drop() error {
	var cur librespot.AudioSource
	if o.source != nil {
		o.source.cond.L.Lock()
		cur = o.source.source[o.source.which]
		o.source.cond.L.Unlock()
	}

	o.mu.Lock()
	o.calls = append(o.calls, "Drop")
	o.dropPrimary = append(o.dropPrimary, cur)
	o.mu.Unlock()
	return nil
}

func (o *recordingOutput) DelayMs() (int64, error) { return 0, nil }
func (o *recordingOutput) SetVolume(float32)       {}
func (o *recordingOutput) Error() <-chan error     { return make(chan error) }
func (o *recordingOutput) Close() error            { o.record("Close"); return nil }

// snapshot returns a copy of the recorded calls so far, safe to inspect
// without racing the manage loop.
func (o *recordingOutput) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...)
}

// newTestPlayer builds a Player driven by manageLoop with newOutput wired to
// the given recordingOutput, bypassing NewPlayer (which requires a real
// spclient/session) since manageLoop only touches p.log, p.cmd, p.ev,
// p.crossfadeSamples and p.newOutput.
func newTestPlayer(t *testing.T, out *recordingOutput) *Player {
	t.Helper()

	p := &Player{
		log: &librespot.NullLogger{},
		cmd: make(chan playerCmd),
		ev:  make(chan Event, 128),
		newOutput: func(reader librespot.Float32Reader, volume float32, device string) (output.Output, error) {
			out.source = reader.(*SwitchingAudioSource)
			out.mu.Lock()
			out.devices = append(out.devices, device)
			out.mu.Unlock()
			return out, nil
		},
	}

	go p.manageLoop()
	t.Cleanup(p.Close)

	return p
}

func lastIndex(s []string, v string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == v {
			return i
		}
	}
	return -1
}

// TestLoadDropFlushesBeforeSourceSwitch locks in the issue #292 fix: on a
// track-load command with drop=true, the output must be flushed (Drop) while
// the previous track is still primary, before source.SetPrimary swaps in the
// new one. Flushing afterwards raced with the new source being read into the
// buffer, clipping the first samples of the incoming track.
func TestLoadDropFlushesBeforeSourceSwitch(t *testing.T) {
	a := rampSource(100, 0, 0)
	b := rampSource(100, 1000, 0)

	out := &recordingOutput{}
	p := newTestPlayer(t, out)

	if err := p.SetPrimaryStream(a, false, false); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	if err := p.SetPrimaryStream(b, false, true); err != nil {
		t.Fatalf("drop load failed: %v", err)
	}

	calls := out.snapshot()

	out.mu.Lock()
	dropPrimary := append([]librespot.AudioSource(nil), out.dropPrimary...)
	out.mu.Unlock()

	// Only the second load requested a drop, so exactly one Drop call should
	// have been recorded.
	if len(dropPrimary) != 1 {
		t.Fatalf("expected exactly one Drop call, got %d (calls=%v)", len(dropPrimary), calls)
	}
	if dropPrimary[0] != librespot.AudioSource(a) {
		t.Fatalf("expected Drop to observe 'a' still primary (i.e. run before the switch to 'b'), got %v", dropPrimary[0])
	}

	// Sanity: by the time the command returned, the switch had happened.
	out.source.cond.L.Lock()
	cur := out.source.source[out.source.which]
	out.source.cond.L.Unlock()
	if cur != librespot.AudioSource(b) {
		t.Fatalf("expected primary source to be 'b' after the load completed, got %v", cur)
	}
}

// TestSeekResumesOnlyWhilePlaying locks in the seek behavior change: Drop no
// longer self-restarts the stream, so a seek while playing must explicitly
// call Resume after Drop, but a seek while paused must not resume playback.
func TestSeekResumesOnlyWhilePlaying(t *testing.T) {
	a := rampSource(1000, 0, 0)

	out := &recordingOutput{}
	p := newTestPlayer(t, out)

	if err := p.SetPrimaryStream(a, false, false); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Seek while playing: Resume must immediately follow Drop.
	if err := p.SeekMs(500); err != nil {
		t.Fatalf("seek while playing failed: %v", err)
	}

	calls := out.snapshot()
	dropIdx, resumeIdx := lastIndex(calls, "Drop"), lastIndex(calls, "Resume")
	if dropIdx == -1 || resumeIdx != dropIdx+1 {
		t.Fatalf("expected Resume to immediately follow Drop while playing, got calls=%v", calls)
	}

	// Pause, then seek again: this time Resume must NOT be called.
	if err := p.Pause(); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	preSeekLen := len(out.snapshot())

	if err := p.SeekMs(700); err != nil {
		t.Fatalf("seek while paused failed: %v", err)
	}

	callsAfter := out.snapshot()[preSeekLen:]
	if slices.Contains(callsAfter, "Resume") {
		t.Fatalf("seek while paused must not resume playback, got calls=%v", callsAfter)
	}
	if !slices.Contains(callsAfter, "Drop") {
		t.Fatalf("expected Drop during a paused seek, got calls=%v", callsAfter)
	}
}

// TestReopenOutputSwitchesDeviceLive locks in the output-recovery behavior:
// ReopenOutput while playing must flush and close the old output, open a new
// one on the requested device, and resume playback — all without touching the
// Spotify source (the same primary stream stays selected).
func TestReopenOutputSwitchesDeviceLive(t *testing.T) {
	a := rampSource(1000, 0, 0)

	out := &recordingOutput{}
	p := newTestPlayer(t, out)

	if err := p.SetPrimaryStream(a, false, false); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	preLen := len(out.snapshot())

	if err := p.ReopenOutput("hifi"); err != nil {
		t.Fatalf("reopen failed: %v", err)
	}

	calls := out.snapshot()[preLen:]
	dropIdx := slices.Index(calls, "Drop")
	closeIdx := slices.Index(calls, "Close")
	resumeIdx := slices.Index(calls, "Resume")
	if dropIdx == -1 || closeIdx == -1 || resumeIdx == -1 {
		t.Fatalf("expected Drop, Close and Resume during a live reopen, got calls=%v", calls)
	}
	if !(dropIdx < closeIdx && closeIdx < resumeIdx) {
		t.Fatalf("expected Drop -> Close -> Resume order during reopen, got calls=%v", calls)
	}

	// The output must have been reopened on the requested device.
	out.mu.Lock()
	devices := append([]string(nil), out.devices...)
	out.mu.Unlock()
	if len(devices) < 2 || devices[len(devices)-1] != "hifi" {
		t.Fatalf("expected the output to be reopened on device \"hifi\", got devices=%v", devices)
	}

	// The primary source must be unchanged: reopening the output does not touch
	// the Spotify session.
	out.source.cond.L.Lock()
	cur := out.source.source[out.source.which]
	out.source.cond.L.Unlock()
	if cur != librespot.AudioSource(a) {
		t.Fatalf("expected the primary source to be unchanged after reopen, got %v", cur)
	}
}

// TestReopenOutputPausedStaysPaused ensures a reopen while paused reopens the
// device but does NOT resume playback.
func TestReopenOutputPausedStaysPaused(t *testing.T) {
	a := rampSource(1000, 0, 0)

	out := &recordingOutput{}
	p := newTestPlayer(t, out)

	if err := p.SetPrimaryStream(a, true, false); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	preLen := len(out.snapshot())

	if err := p.ReopenOutput("hifi"); err != nil {
		t.Fatalf("reopen failed: %v", err)
	}

	calls := out.snapshot()[preLen:]
	if slices.Contains(calls, "Resume") {
		t.Fatalf("reopen while paused must not resume playback, got calls=%v", calls)
	}
	if !slices.Contains(calls, "Close") {
		t.Fatalf("expected the old output to be closed during reopen, got calls=%v", calls)
	}
	if !slices.Contains(calls, "Pause") {
		t.Fatalf("expected the reopened output to be paused, got calls=%v", calls)
	}
}

// TestReopenOutputNothingPlaying ensures a reopen with no output open is a
// no-op that just records the device for the next output open.
func TestReopenOutputNothingPlaying(t *testing.T) {
	out := &recordingOutput{}
	p := newTestPlayer(t, out)

	if err := p.ReopenOutput("hifi"); err != nil {
		t.Fatalf("reopen with nothing playing failed: %v", err)
	}

	if calls := out.snapshot(); len(calls) != 0 {
		t.Fatalf("expected no output calls when reopening with nothing playing, got calls=%v", calls)
	}

	// The device only takes effect on the next output open: loading a stream now
	// must open on the recorded device.
	a := rampSource(100, 0, 0)
	if err := p.SetPrimaryStream(a, false, false); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	out.mu.Lock()
	devices := append([]string(nil), out.devices...)
	out.mu.Unlock()
	if len(devices) != 1 || devices[0] != "hifi" {
		t.Fatalf("expected the next output to open on device \"hifi\", got devices=%v", devices)
	}
}
