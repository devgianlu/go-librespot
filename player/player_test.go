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
		newOutput: func(reader librespot.Float32Reader, volume float32) (output.Output, error) {
			out.source = reader.(*SwitchingAudioSource)
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
