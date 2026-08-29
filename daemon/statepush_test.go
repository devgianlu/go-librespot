//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
)

// newTestStatePushLane builds a lane whose pushes are answered by put rather
// than by the network. The lane goroutine is not started: the queueing tests
// drive submit and next by hand so they do not have to race it.
func newTestStatePushLane(put func(context.Context, string, connectpb.PutStateReason, []byte) (*connectpb.Cluster, error)) *statePushLane {
	return &statePushLane{
		log:     &librespot.NullLogger{},
		put:     put,
		wake:    make(chan struct{}, 1),
		results: make(chan statePushResult, statePushQueueSize),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func push(seq uint64, reason connectpb.PutStateReason) statePush {
	return statePush{seq: seq, reason: reason}
}

func drain(l *statePushLane) []statePush {
	var out []statePush
	for {
		p, ok := l.next()
		if !ok {
			return out
		}
		out = append(out, p)
	}
}

// Consecutive player-state pushes describe the same thing at different moments,
// so only the last of a run is worth sending.
func TestStatePushCoalescesConsecutivePlayerState(t *testing.T) {
	l := newTestStatePushLane(nil)

	for seq := range uint64(5) {
		l.submit(push(seq, connectpb.PutStateReason_PLAYER_STATE_CHANGED))
	}

	queued := drain(l)
	require.Len(t, queued, 1)
	require.Equal(t, uint64(4), queued[0].seq, "the newest state wins")
}

// Every other reason marks a transition the backend has to see, and in order.
func TestStatePushKeepsTransitions(t *testing.T) {
	l := newTestStatePushLane(nil)

	l.submit(push(1, connectpb.PutStateReason_NEW_DEVICE))
	l.submit(push(2, connectpb.PutStateReason_PLAYER_STATE_CHANGED))
	l.submit(push(3, connectpb.PutStateReason_PLAYER_STATE_CHANGED))
	l.submit(push(4, connectpb.PutStateReason_VOLUME_CHANGED))
	l.submit(push(5, connectpb.PutStateReason_BECAME_INACTIVE))

	queued := drain(l)
	require.Len(t, queued, 4)
	require.Equal(t, uint64(1), queued[0].seq)
	require.Equal(t, uint64(3), queued[1].seq, "only the player-state run coalesced")
	require.Equal(t, uint64(4), queued[2].seq)
	require.Equal(t, uint64(5), queued[3].seq)
}

// A backlog of transitions must not push the newest one out; the oldest
// superseded player state goes instead.
func TestStatePushOverflowDropsSupersededState(t *testing.T) {
	l := newTestStatePushLane(nil)

	l.submit(push(1, connectpb.PutStateReason_PLAYER_STATE_CHANGED))
	for seq := uint64(2); seq <= uint64(statePushQueueSize+1); seq++ {
		l.submit(push(seq, connectpb.PutStateReason_VOLUME_CHANGED))
	}

	queued := drain(l)
	require.Len(t, queued, statePushQueueSize)
	require.Equal(t, uint64(2), queued[0].seq, "the stale player state was dropped")
	require.Equal(t, uint64(statePushQueueSize+1), queued[len(queued)-1].seq)
}

// Submitting must never wait on the push in flight — the caller is the player
// loop, and that is the whole reason this lane exists.
func TestStatePushSubmitDoesNotBlockOnAStalledPut(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once

	l := newTestStatePushLane(func(ctx context.Context, _ string, _ connectpb.PutStateReason, _ []byte) (*connectpb.Cluster, error) {
		once.Do(func() {
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		return nil, nil
	})
	go l.run()

	l.submit(push(1, connectpb.PutStateReason_NEW_DEVICE))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for seq := uint64(2); seq < 100; seq++ {
			l.submit(push(seq, connectpb.PutStateReason_PLAYER_STATE_CHANGED))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submit blocked behind a push in flight")
	}

	close(release)
	l.close()
}

// Closing must not leave the caller waiting on a push that will never answer.
func TestStatePushCloseCancelsInFlight(t *testing.T) {
	started := make(chan struct{})

	l := newTestStatePushLane(func(ctx context.Context, _ string, _ connectpb.PutStateReason, _ []byte) (*connectpb.Cluster, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go l.run()

	l.submit(push(1, connectpb.PutStateReason_PLAYER_STATE_CHANGED))
	<-started

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		l.close()
	}()

	select {
	case <-closed:
	case <-time.After(statePushDrainTimeout + time.Second):
		t.Fatal("close did not cancel the push in flight")
	}

	l.close() // idempotent
}

// A failure only re-arms the retry when it describes the newest state; an older
// push has already been superseded by the one behind it.
func TestApplyStatePushResultRetriesOnlyTheNewest(t *testing.T) {
	p := &AppPlayer{app: &App{log: &librespot.NullLogger{}}, stateSeq: 7}
	p.stateTimer = time.NewTimer(time.Hour)
	p.stateTimer.Stop()

	p.applyStatePushResult(statePushResult{seq: 6, err: errors.New("boom")})
	require.False(t, p.stateDirty)
	require.False(t, p.statePutScheduled)

	p.applyStatePushResult(statePushResult{seq: 7, err: errors.New("boom")})
	require.True(t, p.stateDirty, "the newest state must be resent")
	require.True(t, p.statePutScheduled)
	require.Equal(t, 1, p.stateRetries)
}

// Registering the device is what makes playback ready, and any push does it —
// so a failed initial push does not leave the daemon permanently not-ready.
func TestApplyStatePushResultMarksDeviceRegistered(t *testing.T) {
	stub, err := NewStubApiServer(&librespot.NullLogger{})
	require.NoError(t, err)

	p := &AppPlayer{
		app:             &App{log: &librespot.NullLogger{}, server: stub},
		state:           &State{device: &connectpb.DeviceInfo{}},
		playbackReadyCh: make(chan struct{}),
		stateSeq:        2,
	}
	p.stateTimer = time.NewTimer(time.Hour)
	p.stateTimer.Stop()

	p.applyStatePushResult(statePushResult{seq: 1, reason: connectpb.PutStateReason_NEW_DEVICE, err: errors.New("boom")})
	require.False(t, p.hasInitialConnectState)

	p.applyStatePushResult(statePushResult{seq: 2, reason: connectpb.PutStateReason_PLAYER_STATE_CHANGED, publicIp: "203.0.113.7"})
	require.True(t, p.hasInitialConnectState)
	require.Equal(t, "203.0.113.7", p.state.device.PublicIp, "the cluster's view of our address comes home")
	require.Equal(t, 0, p.stateRetries)
}
