//go:build test_unit

package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestSession() *activeSession {
	return &activeSession{
		apiCh: make(chan ApiRequest),
		done:  make(chan struct{}),
	}
}

func TestActiveSessionForwardDelivers(t *testing.T) {
	s := newTestSession()

	received := make(chan ApiRequest, 1)
	go func() { received <- <-s.apiCh }()

	require.True(t, s.forward(context.Background(), ApiRequest{Type: ApiRequestTypeStatus}))

	select {
	case req := <-received:
		require.Equal(t, ApiRequestTypeStatus, req.Type)
	case <-time.After(time.Second):
		t.Fatal("request was not delivered to the player")
	}
}

func TestActiveSessionForwardGivesUpOnceRetired(t *testing.T) {
	s := newTestSession()
	s.retire()

	// Nothing reads apiCh, so without the done case this would block forever.
	require.False(t, s.forward(context.Background(), ApiRequest{}))
}

// A request can already be on its way into a session that is being torn down.
// The sender has to be released rather than left blocked on a channel with no
// reader — and closing apiCh instead would panic the whole daemon.
func TestActiveSessionRetireReleasesPendingForward(t *testing.T) {
	s := newTestSession()

	result := make(chan bool, 1)
	go func() { result <- s.forward(context.Background(), ApiRequest{}) }()

	s.retire()

	select {
	case delivered := <-result:
		require.False(t, delivered)
	case <-time.After(2 * time.Second):
		t.Fatal("forward stayed blocked after the session was retired")
	}
}

func TestActiveSessionForwardGivesUpOnContextCancel(t *testing.T) {
	s := newTestSession()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.False(t, s.forward(ctx, ApiRequest{}))
}

// Teardown and a Run that returns on its own both retire the session, so it
// happens twice in the ordinary case and must stay harmless.
func TestActiveSessionRetireIsIdempotent(t *testing.T) {
	s := newTestSession()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			s.retire()
		})
	}
	wg.Wait()

	select {
	case <-s.done:
	default:
		t.Fatal("session was not retired")
	}
}

// Forwarding races the teardown by design: neither a panic nor a stuck sender
// is acceptable, whichever order they land in.
func TestActiveSessionConcurrentForwardAndRetire(t *testing.T) {
	for range 200 {
		s := newTestSession()

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			s.forward(context.Background(), ApiRequest{})
		}()
		go func() {
			defer wg.Done()
			s.retire()
		}()

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("forward and retire deadlocked")
		}
	}
}
