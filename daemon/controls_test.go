package daemon

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/tracks"
)

// recordingApiServer is an ApiServer that records emitted events, so tests can
// assert on the event sequence without a websocket.
type recordingApiServer struct {
	mu     sync.Mutex
	events []ApiEventType
}

func (s *recordingApiServer) Emit(ev *ApiEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev.Type)
	s.mu.Unlock()
}

func (s *recordingApiServer) Receive() <-chan ApiRequest { return make(<-chan ApiRequest) }
func (s *recordingApiServer) Close() error               { return nil }

func (s *recordingApiServer) snapshot() []ApiEventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ApiEventType(nil), s.events...)
}

// newSettleTestPlayer builds the minimal AppPlayer needed by the settle
// bookkeeping helpers (shouldDeferSkip, deferSettle, settleNow's no-context
// path, cancelSettle): config, recording event server, state and stopped
// timers. lastStatePut is set to now so updateState defers its PUT to the
// state timer instead of reaching for a live session.
func newSettleTestPlayer(t *testing.T, debounce time.Duration) (*AppPlayer, *recordingApiServer) {
	t.Helper()

	server := &recordingApiServer{}
	p := &AppPlayer{
		app: &App{
			cfg:    &Config{SkipDebounce: debounce},
			server: server,
			log:    &librespot.NullLogger{},
		},
		state: &State{
			player: &connectpb.PlayerState{
				PlayOrigin: &connectpb.PlayOrigin{FeatureIdentifier: "go-librespot"},
				Track:      &connectpb.ProvidedTrack{Uri: "spotify:track:pending"},
			},
		},
		lastStatePut: time.Now(),
	}

	p.settleTimer = time.NewTimer(math.MaxInt64)
	p.settleTimer.Stop()
	p.stateTimer = time.NewTimer(math.MaxInt64)
	p.stateTimer.Stop()

	return p, server
}

func TestShouldDeferSkip(t *testing.T) {
	cases := []struct {
		name          string
		debounce      time.Duration
		hasContext    bool
		settlePending bool
		lastSkipDone  time.Time
		want          bool
	}{
		{"disabled never defers", 0, true, true, time.Now(), false},
		{"no context never defers", 400 * time.Millisecond, false, true, time.Now(), false},
		{"first skip is immediate", 400 * time.Millisecond, true, false, time.Time{}, false},
		{"skip after quiet period is immediate", 400 * time.Millisecond, true, false, time.Now().Add(-time.Second), false},
		{"pending settle defers", 400 * time.Millisecond, true, true, time.Time{}, true},
		{"skip right after previous defers", 400 * time.Millisecond, true, false, time.Now(), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newSettleTestPlayer(t, tc.debounce)
			if tc.hasContext {
				p.state.tracks = &tracks.List{}
			}
			p.settlePending = tc.settlePending
			p.lastSkipDone = tc.lastSkipDone

			if got := p.shouldDeferSkip(); got != tc.want {
				t.Fatalf("shouldDeferSkip() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestDeferSettlePublishesPendingTrack(t *testing.T) {
	p, server := newSettleTestPlayer(t, 400*time.Millisecond)

	p.deferSettle(context.Background())

	if !p.settlePending {
		t.Fatal("expected settlePending after deferSettle")
	}
	if !p.state.player.IsPlaying || !p.state.player.IsBuffering {
		t.Fatalf("expected the pending track to be published as playing+buffering, got playing=%t buffering=%t",
			p.state.player.IsPlaying, p.state.player.IsBuffering)
	}
	if time.Since(p.lastSkipDone) > time.Second {
		t.Fatal("expected lastSkipDone to be refreshed")
	}

	events := server.snapshot()
	if len(events) != 1 || events[0] != ApiEventTypeWillPlay {
		t.Fatalf("expected exactly one will_play event, got %v", events)
	}
}

// A burst moves the pointer far faster than the connect-state endpoint tolerates,
// so only the first deferral publishes; the rest are visible locally through
// will_play and are superseded before anyone could have seen them remotely.
func TestDeferSettlePutsConnectStateOncePerBurst(t *testing.T) {
	p, server := newSettleTestPlayer(t, 400*time.Millisecond)

	p.deferSettle(context.Background())
	if !p.statePutScheduled {
		t.Fatal("expected the first deferral of a burst to publish the connect state")
	}

	// Pretend the scheduled PUT went out, then keep skipping within the window.
	p.statePutScheduled = false
	p.stateDirty = false

	for i := 0; i < 5; i++ {
		p.deferSettle(context.Background())
	}

	if p.statePutScheduled || p.stateDirty {
		t.Fatalf("expected no further connect-state PUTs during the burst, got scheduled=%t dirty=%t",
			p.statePutScheduled, p.stateDirty)
	}

	if events := server.snapshot(); len(events) != 6 {
		t.Fatalf("expected a will_play event for every pointer move, got %v", events)
	}
}

func TestSettleNowWithoutContextIsNoop(t *testing.T) {
	p, server := newSettleTestPlayer(t, 400*time.Millisecond)
	p.settlePending = true
	p.settleAtEnd = false

	if err := p.settleNow(context.Background()); err != nil {
		t.Fatalf("settleNow without context failed: %v", err)
	}
	if p.settlePending || p.settleAtEnd {
		t.Fatal("expected settle flags cleared")
	}
	if events := server.snapshot(); len(events) != 0 {
		t.Fatalf("expected no events, got %v", events)
	}
}

// TestSettleNowAtEndWithoutContext locks in the no-context end-of-context
// path: a deferred skip past the end with no track list must not panic and
// must tell clients playback stopped.
func TestSettleNowAtEndWithoutContext(t *testing.T) {
	p, server := newSettleTestPlayer(t, 400*time.Millisecond)
	p.state.player.Track = nil // fresh state: no track ever loaded
	p.settlePending = true
	p.settleAtEnd = true

	if err := p.settleNow(context.Background()); err != nil {
		t.Fatalf("settleNow at end without context failed: %v", err)
	}
	if p.settlePending || p.settleAtEnd {
		t.Fatal("expected settle flags cleared")
	}

	events := server.snapshot()
	if len(events) != 1 || events[0] != ApiEventTypeStopped {
		t.Fatalf("expected exactly one stopped event, got %v", events)
	}
}

// TestSettleWaitIsNotPlayback locks in the position fix: the time spent
// waiting for the settle timer must not count as playback, otherwise the
// settled track starts ~debounce ms into the song instead of at 0:00.
func TestSettleWaitIsNotPlayback(t *testing.T) {
	p, _ := newSettleTestPlayer(t, 400*time.Millisecond)

	// State as a deferred skip leaves it, with the settle wait elapsed.
	p.deferSettle(context.Background())
	p.state.player.Timestamp = time.Now().Add(-450 * time.Millisecond).UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	// No context: settleNow returns early, but must have refreshed the
	// timestamp first so the wait is discarded.
	if err := p.settleNow(context.Background()); err != nil {
		t.Fatalf("settleNow failed: %v", err)
	}

	if pos := p.state.trackPosition(); pos > 50 {
		t.Fatalf("expected the settle wait to be discarded from the position, got %dms", pos)
	}
}

func TestCancelSettleClearsEverything(t *testing.T) {
	p, _ := newSettleTestPlayer(t, 400*time.Millisecond)
	p.settlePending = true
	p.settleAtEnd = true
	p.settleTimer.Reset(time.Hour)

	p.cancelSettle()

	if p.settlePending || p.settleAtEnd {
		t.Fatalf("expected all settle state cleared, got pending=%t atEnd=%t",
			p.settlePending, p.settleAtEnd)
	}
}
