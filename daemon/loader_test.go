//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	"github.com/stretchr/testify/require"
)

// newTestLoaderLane builds a lane without starting its goroutine, so the
// queueing tests can drive submit and next by hand rather than race it.
func newTestLoaderLane() *loaderLane {
	ctx, cancel := context.WithCancel(context.Background())
	return &loaderLane{
		log:     &librespot.NullLogger{},
		ctx:     ctx,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
		results: make(chan loaderResult, 4),
		done:    make(chan struct{}),
	}
}

// recordingReply captures what a job was answered with, and fails the test if it
// is answered twice.
func recordingReply(t *testing.T) (replyTo, func() (error, bool)) {
	t.Helper()

	var (
		mu     sync.Mutex
		got    error
		called int
	)

	reply := replyTo{once: new(sync.Once), fn: func(_ any, err error) {
		mu.Lock()
		defer mu.Unlock()
		called++
		got = err
	}}

	return reply, func() (error, bool) {
		mu.Lock()
		defer mu.Unlock()
		require.LessOrEqual(t, called, 1, "a reply channel is written exactly once")
		return got, called == 1
	}
}

func idleJob(name string, class loaderClass, reply replyTo) loaderJob {
	return loaderJob{
		name:  name,
		class: class,
		reply: reply,
		run:   func(context.Context) loaderResult { return loaderResult{} },
	}
}

func queuedNames(l *loaderLane) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	names := make([]string, 0, len(l.queue))
	for _, job := range l.queue {
		names = append(names, job.name)
	}
	return names
}

// Only the newest destination matters. Running the superseded ones first is the
// burst of skipping reported in #300.
func TestLoaderNewLoadDropsOlderLoadsAndPrefetches(t *testing.T) {
	l := newTestLoaderLane()

	prefetchReply, prefetchAnswered := recordingReply(t)
	firstReply, firstAnswered := recordingReply(t)

	l.submit(idleJob("prefetch a", classPrefetch, prefetchReply))
	l.submit(idleJob("load a", classLoad, firstReply))
	l.submit(idleJob("load b", classLoad, noReply))

	require.Equal(t, []string{"load b"}, queuedNames(l))

	err, answered := prefetchAnswered()
	require.True(t, answered, "a dropped job still owes its caller an answer")
	require.ErrorIs(t, err, ErrSuperseded)

	err, answered = firstAnswered()
	require.True(t, answered)
	require.ErrorIs(t, err, ErrSuperseded)
}

// Losing a queued track would be visible, so queue edits are never dropped.
func TestLoaderKeepsMutations(t *testing.T) {
	l := newTestLoaderLane()

	l.submit(idleJob("queue a", classMutate, noReply))
	l.submit(idleJob("load a", classLoad, noReply))
	l.submit(idleJob("queue b", classMutate, noReply))
	l.submit(idleJob("load b", classLoad, noReply))

	require.Equal(t, []string{"queue a", "queue b", "load b"}, queuedNames(l),
		"mutations survive in order, and only the newest load remains")
}

// A prefetch is best effort: with a load pending it is not worth doing at all,
// since the load decides what comes next.
func TestLoaderPrefetchYieldsToAPendingLoad(t *testing.T) {
	l := newTestLoaderLane()

	reply, answered := recordingReply(t)

	l.submit(idleJob("load a", classLoad, noReply))
	l.submit(idleJob("prefetch a", classPrefetch, reply))

	require.Equal(t, []string{"load a"}, queuedNames(l))

	err, called := answered()
	require.True(t, called)
	require.NoError(t, err, "yielding is not a failure")
}

// A step forward or back is not a destination: dropping one changes where the
// burst ends up, so navigation is queued through later navigation and loads
// alike, while a load queued ahead of it is no longer wanted.
func TestLoaderNavigationIsNotDroppedByLaterWork(t *testing.T) {
	l := newTestLoaderLane()

	loadReply, loadAnswered := recordingReply(t)
	navReply, navAnswered := recordingReply(t)

	l.submit(idleJob("load a", classLoad, loadReply))
	l.submit(idleJob("next", classNav, navReply))
	l.submit(idleJob("next", classNav, noReply))
	l.submit(idleJob("load b", classLoad, noReply))

	require.Equal(t, []string{"next", "next", "load b"}, queuedNames(l))

	err, answered := loadAnswered()
	require.True(t, answered, "the load ahead of a skip is no longer wanted")
	require.ErrorIs(t, err, ErrSuperseded)

	_, answered = navAnswered()
	require.False(t, answered, "a queued skip is never dropped")
}

// The load in flight was fetching a track the pointer is about to leave, so a
// skip cancels it like a load would; a walk in flight is a step that must
// still be taken, so nothing cancels that.
func TestLoaderNavigationCancelsOnlyFetches(t *testing.T) {
	l := newTestLoaderLane()

	loadCtx, cancel := context.WithCancel(context.Background())
	l.running = &runningJob{class: classLoad, cancel: cancel}
	l.submit(idleJob("next", classNav, noReply))
	require.ErrorIs(t, loadCtx.Err(), context.Canceled, "the running load was not cancelled by a skip")

	navCtx, cancel := context.WithCancel(context.Background())
	l.running = &runningJob{class: classNav, cancel: cancel}
	l.submit(idleJob("next", classNav, noReply))
	l.submit(idleJob("load", classLoad, noReply))
	require.NoError(t, navCtx.Err(), "a walk in flight is never cancelled")
}

// Regression: relative walks used to be plain loads, so a press that arrived
// while the previous one was still queued dropped it, and a burst of twenty
// presses could end fewer than twenty tracks on. Every queued step must run,
// in order.
func TestLoaderBackToBackSkipsBothStep(t *testing.T) {
	l := newLoaderLane(&librespot.NullLogger{})
	t.Cleanup(l.close)

	var (
		mu    sync.Mutex
		steps []string
	)
	step := func(name string) loaderJob {
		return loaderJob{
			name:  name,
			class: classNav,
			run: func(context.Context) loaderResult {
				mu.Lock()
				defer mu.Unlock()
				steps = append(steps, name)
				return loaderResult{}
			},
		}
	}

	l.submit(step("first"))
	l.submit(step("second"))

	for range 2 {
		select {
		case <-l.results:
		case <-time.After(5 * time.Second):
			t.Fatal("a queued skip never ran")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"first", "second"}, steps)
}

// A held load stays queued until its time, and only then is taken.
func TestLoaderHeldLoadWaitsForItsTime(t *testing.T) {
	l := newTestLoaderLane()

	held := idleJob("load", classLoad, noReply)
	held.notBefore = time.Now().Add(time.Hour)
	l.submit(held)

	_, _, wait, ok := l.next()
	require.False(t, ok)
	require.Greater(t, wait, time.Duration(0), "the lane must be told when to look again")
	require.Equal(t, []string{"load"}, queuedNames(l), "a held load stays queued")

	l.queue[0].notBefore = time.Now().Add(-time.Second)
	_, _, _, ok = l.next()
	require.True(t, ok, "a load whose time has come is taken")
}

// The point of holding a load: the next press, or stopping playback, drops it
// before it has cost anything.
func TestLoaderHeldLoadIsDroppedUnstarted(t *testing.T) {
	for name, supersede := range map[string]func(*loaderLane){
		"by a skip": func(l *loaderLane) { l.submit(idleJob("next", classNav, noReply)) },
		"by a load": func(l *loaderLane) { l.submit(idleJob("load b", classLoad, noReply)) },
		"by a stop": func(l *loaderLane) { l.dropLoads() },
	} {
		t.Run(name, func(t *testing.T) {
			l := newTestLoaderLane()

			reply, answered := recordingReply(t)
			held := idleJob("load a", classLoad, reply)
			held.notBefore = time.Now().Add(time.Hour)
			l.submit(held)

			supersede(l)

			require.NotContains(t, queuedNames(l), "load a")

			err, called := answered()
			require.True(t, called, "a dropped job still owes its caller an answer")
			require.ErrorIs(t, err, ErrSuperseded)
		})
	}
}

// Nothing arrives to wake the lane while a load is held, so it has to wake
// itself once the time comes.
func TestLoaderHeldLoadRunsOnceItsTimeComes(t *testing.T) {
	l := newLoaderLane(&librespot.NullLogger{})
	t.Cleanup(l.close)

	notBefore := time.Now().Add(50 * time.Millisecond)

	var started time.Time
	l.submit(loaderJob{
		name:      "load",
		class:     classLoad,
		notBefore: notBefore,
		run: func(context.Context) loaderResult {
			started = time.Now()
			return loaderResult{}
		},
	})

	select {
	case <-l.results:
	case <-time.After(5 * time.Second):
		t.Fatal("the held load never ran")
	}

	require.False(t, started.Before(notBefore), "the load ran before its time")
}

// A load still held at shutdown owes its caller an answer like anything else
// queued.
func TestLoaderCloseAnswersAHeldLoad(t *testing.T) {
	l := newLoaderLane(&librespot.NullLogger{})

	reply, answered := recordingReply(t)
	held := idleJob("load", classLoad, reply)
	held.notBefore = time.Now().Add(time.Hour)
	l.submit(held)

	l.close()

	err, called := answered()
	require.True(t, called)
	require.ErrorIs(t, err, ErrNoSession)
}

// A skip ends in a load, so fetching ahead while one is queued is as pointless
// as with a load queued.
func TestLoaderPrefetchYieldsToQueuedNavigation(t *testing.T) {
	l := newTestLoaderLane()

	reply, answered := recordingReply(t)

	l.submit(idleJob("next", classNav, noReply))
	l.submit(idleJob("prefetch a", classPrefetch, reply))

	require.Equal(t, []string{"next"}, queuedNames(l))

	err, called := answered()
	require.True(t, called)
	require.NoError(t, err)
}

// Submitting must never wait on the job in flight: the caller is the player loop.
func TestLoaderSubmitDoesNotBlockOnAStalledJob(t *testing.T) {
	l := newLoaderLane(&librespot.NullLogger{})
	t.Cleanup(l.close)

	started := make(chan struct{})
	l.submit(loaderJob{
		name:  "stalled",
		class: classMutate,
		run: func(ctx context.Context) loaderResult {
			close(started)
			<-ctx.Done()
			return loaderResult{err: ctx.Err()}
		},
	})
	<-started

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			l.submit(idleJob("load", classLoad, noReply))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submit blocked behind the job in flight")
	}
}

// A newer load cancels the one running rather than waiting it out, so a stalled
// fetch cannot delay the track the listener actually asked for.
func TestLoaderNewLoadCancelsTheJobInFlight(t *testing.T) {
	l := newLoaderLane(&librespot.NullLogger{})
	t.Cleanup(l.close)

	started := make(chan struct{})
	cancelled := make(chan struct{})

	l.submit(loaderJob{
		name:  "stalled load",
		class: classLoad,
		run: func(ctx context.Context) loaderResult {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return loaderResult{err: ctx.Err()}
		},
	})
	<-started

	l.submit(idleJob("newer load", classLoad, noReply))

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the running load was not cancelled by a newer one")
	}
}

// Closing must answer everything still owed, or its callers wait forever.
func TestLoaderCloseAnswersQueuedJobs(t *testing.T) {
	l := newLoaderLane(&librespot.NullLogger{})

	started := make(chan struct{})
	l.submit(loaderJob{
		name:  "running",
		class: classMutate,
		run: func(ctx context.Context) loaderResult {
			close(started)
			<-ctx.Done()
			return loaderResult{err: ctx.Err()}
		},
	})
	<-started

	reply, answered := recordingReply(t)
	l.submit(idleJob("queued", classMutate, reply))

	l.close()
	l.close() // idempotent

	err, called := answered()
	require.True(t, called, "a job still queued at shutdown owes its caller an answer")
	require.ErrorIs(t, err, ErrNoSession)
}

// Stopping playback abandons every load and prefetch, running or queued, and
// leaves queue edits alone.
func TestLoaderDropLoadsCancelsAndKeepsMutations(t *testing.T) {
	l := newTestLoaderLane()

	// Stand in for a load in flight, so the cancellation can be observed
	// without racing the lane goroutine for the queue.
	ctx, cancel := context.WithCancel(context.Background())
	l.running = &runningJob{class: classLoad, cancel: cancel}

	loadReply, loadAnswered := recordingReply(t)
	l.submit(idleJob("queue a", classMutate, noReply))
	l.submit(idleJob("prefetch a", classPrefetch, noReply))
	l.submit(idleJob("load a", classLoad, loadReply))

	l.dropLoads()

	require.ErrorIs(t, ctx.Err(), context.Canceled, "the running load was not cancelled")
	require.Equal(t, []string{"queue a"}, queuedNames(l))

	err, answered := loadAnswered()
	require.True(t, answered, "a dropped job still owes its caller an answer")
	require.ErrorIs(t, err, ErrSuperseded)
}

// A superseded result must release whatever its job opened: a built stream owns
// an open CDN reader or cache file that nothing else will close.
func TestApplyLoaderResultDiscardsSupersededWork(t *testing.T) {
	p := &AppPlayer{app: &App{log: &librespot.NullLogger{}}, loadGen: 3}

	var discarded, committed bool
	reply, answered := recordingReply(t)

	p.applyLoaderResult(loaderResult{
		gen:     2,
		reply:   reply,
		commit:  func(*AppPlayer, error) { committed = true },
		discard: func() { discarded = true },
	})

	require.True(t, discarded)
	require.False(t, committed, "state from a superseded job must not be applied")

	err, called := answered()
	require.True(t, called)
	require.ErrorIs(t, err, ErrSuperseded)
}

// A queue edit or option change invalidates what was prefetched, not the track
// being loaded: the load in flight must still land, or the player is left
// playing a stream the loop never records.
func TestApplyLoaderResultKeepsLoadAcrossPrefetchInvalidation(t *testing.T) {
	p := &AppPlayer{app: &App{log: &librespot.NullLogger{}}, loadGen: 3, prefetchGen: 1, loadInFlight: true}

	// What invalidateUpcoming does to the counters.
	p.prefetchGen++

	var loadCommitted, prefetchCommitted, prefetchDiscarded bool
	p.applyLoaderResult(loaderResult{
		class:   classPrefetch,
		gen:     1,
		commit:  func(*AppPlayer, error) { prefetchCommitted = true },
		discard: func() { prefetchDiscarded = true },
	})
	p.applyLoaderResult(loaderResult{
		class:  classLoad,
		gen:    3,
		commit: func(*AppPlayer, error) { loadCommitted = true },
	})

	require.True(t, prefetchDiscarded, "the stale prefetch is thrown away")
	require.False(t, prefetchCommitted)
	require.True(t, loadCommitted, "the load is still wanted")
	require.False(t, p.loadInFlight)
}

// The outgoing stream kept playing while its replacement loaded. If it ran out
// meanwhile, the events reporting that were held for the load; handling them
// once it lands would advance past the track that just started. The handlers
// need a session, so if either event were drained here the test would panic.
func TestApplyLoaderResultForgetsTheReplacedStreamsEnd(t *testing.T) {
	p := &AppPlayer{app: &App{log: &librespot.NullLogger{}}, loadGen: 1, loadInFlight: true}
	p.pendingPlayerEvents = []player.Event{{Type: player.EventTypeNotPlaying}, {Type: player.EventTypeStop}}

	var committed bool
	p.applyLoaderResult(loaderResult{
		class:  classLoad,
		gen:    1,
		commit: func(*AppPlayer, error) { committed = true },
	})

	require.True(t, committed)
	require.False(t, p.loadInFlight)
	require.Empty(t, p.pendingPlayerEvents)
}

// Only the end of the outgoing stream is forgotten: a play or pause that
// arrived during the load describes what the listener asked for meanwhile.
func TestForgetReplacedStreamEventsKeepsTheRest(t *testing.T) {
	p := &AppPlayer{pendingPlayerEvents: []player.Event{
		{Type: player.EventTypePlay},
		{Type: player.EventTypeNotPlaying},
		{Type: player.EventTypePause},
		{Type: player.EventTypeStop},
	}}

	p.forgetReplacedStreamEvents()

	require.Equal(t, []player.Event{{Type: player.EventTypePlay}, {Type: player.EventTypePause}}, p.pendingPlayerEvents)
}

// A job that failed still reaches its commit, so whoever asked for the load
// learns it did not happen, and still owes its caller the failure.
func TestApplyLoaderResultReportsFailure(t *testing.T) {
	p := &AppPlayer{app: &App{log: &librespot.NullLogger{}}, loadGen: 1}

	var committed error
	reply, answered := recordingReply(t)
	boom := errors.New("boom")

	p.applyLoaderResult(loaderResult{
		gen:    1,
		name:   "load",
		err:    boom,
		reply:  reply,
		commit: func(_ *AppPlayer, err error) { committed = err },
	})

	require.ErrorIs(t, committed, boom)

	err, called := answered()
	require.True(t, called)
	require.ErrorIs(t, err, boom)
}
