package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/tracks"
)

var (
	// ErrSuperseded reports that a newer command arrived before this one's
	// network work finished, so its result was thrown away.
	ErrSuperseded = errors.New("superseded by a newer command")

	// ErrLoaderBusy reports that the daemon has more work queued than it can
	// usefully hold.
	ErrLoaderBusy = errors.New("player is busy")
)

// replyTo carries the acknowledgement a command still owes its caller into work
// that finishes elsewhere. Every reply channel in the daemon is buffered once
// and must be written exactly once — a second write blocks forever, and no
// write leaves the caller waiting — so every path out goes through done, which
// guards that rather than trusting each caller to.
type replyTo struct {
	once *sync.Once
	fn   func(data any, err error)
}

// noReply is for work nobody is waiting on.
var noReply replyTo

func (r replyTo) done(data any, err error) {
	if r.fn == nil {
		return
	}

	r.once.Do(func() { r.fn(data, err) })
}

// applyLoaderResult folds a finished job back into the state. Runs on the Run
// goroutine, which is what makes commit safe to write state from.
func (p *AppPlayer) applyLoaderResult(res loaderResult) {
	if res.class == classLoad && res.gen == p.loadGen {
		p.loadInFlight = false
		if res.err == nil {
			p.forgetReplacedStreamEvents()
		}
		defer p.drainPendingPlayerEvents()
	}

	// A mutation is applied whatever else has happened since: it already changed
	// the track list, and refusing to report that would leave the state
	// describing a list that no longer exists. Only decisions about what to play
	// go stale.
	if res.class != classMutate && res.gen != p.generation(res.class) {
		if res.discard != nil {
			res.discard()
		}
		res.reply.done(nil, ErrSuperseded)
		return
	}

	if res.err != nil {
		p.app.log.WithError(res.err).Warnf("failed %s", res.name)
	}
	if res.commit != nil {
		res.commit(p, res.err)
	}

	res.reply.done(res.value, res.err)
}

// generation reports the counter a job of this class is stamped with, so that
// its result can be told from one the loop has since moved past.
func (p *AppPlayer) generation(class loaderClass) uint64 {
	if class == classPrefetch {
		return p.prefetchGen
	}
	return p.loadGen
}

// ErrNoContext reports that a command needing a track list arrived when the
// daemon has no context loaded.
var ErrNoContext = errors.New("no context")

// listJob runs walk with exclusive use of the track list on the loader lane,
// then applies on the player loop what the list looks like afterwards.
//
// The list is reachable from nowhere else: walking it fetches context pages over
// the network, and neither it nor the resolver behind it is safe to touch from
// two goroutines. commit is given the snapshot walk left behind, or the error
// that stopped it.
func (p *AppPlayer) listJob(name string, class loaderClass, nextHint []*connectpb.ContextTrack,
	walk func(ctx context.Context, list *tracks.List) error,
	commit func(p *AppPlayer, snap *tracks.Snapshot, err error),
) {
	list := p.state.tracks
	if list == nil {
		commit(p, nil, ErrNoContext)
		return
	}

	p.loader.submit(loaderJob{
		name:  name,
		class: class,
		gen:   p.generation(class),
		run: func(ctx context.Context) loaderResult {
			if err := walk(ctx, list); err != nil {
				return loaderResult{
					err:    err,
					commit: func(p *AppPlayer, err error) { commit(p, nil, err) },
				}
			}

			snap := list.Snapshot(ctx, nextHint)

			return loaderResult{commit: func(p *AppPlayer, err error) {
				// A context load may have replaced the list while this was
				// queued behind it; describing the old one would report the
				// wrong context entirely.
				if p.state.tracks != list {
					commit(p, nil, ErrSuperseded)
					return
				}

				commit(p, snap, err)
			}}
		},
	})
}

// errReplyDeferred reports that a handler has taken responsibility for
// answering its request itself, once work it started elsewhere finishes.
var errReplyDeferred = errors.New("reply deferred")

// apiReply hands an API request's acknowledgement to work that finishes
// elsewhere. The handler returns errReplyDeferred so the player loop knows not
// to answer it as well.
func apiReply(req ApiRequest) replyTo {
	return replyTo{once: new(sync.Once), fn: req.Reply}
}

// goDetached runs fn off the player loop, for work that touches no player state
// and that nothing here waits on. Whatever fn needs is captured before the call.
func (p *AppPlayer) goDetached(timeout time.Duration, fn func(ctx context.Context)) {
	go func() {
		ctx, cancel := context.WithTimeout(p.ctx, timeout)
		defer cancel()
		fn(ctx)
	}()
}

// tokenTimeout bounds an access token renewal made on behalf of an API caller.
const tokenTimeout = 30 * time.Second

// maxPendingPlayerEvents bounds how many player events are held while a load is
// outstanding, so a load that never lands cannot grow the buffer without limit.
const maxPendingPlayerEvents = 32

// deferPlayerEvent holds an event until the load in flight has been recorded,
// reporting whether it took it.
func (p *AppPlayer) deferPlayerEvent(ev player.Event) bool {
	if !p.loadInFlight {
		return false
	}

	if len(p.pendingPlayerEvents) >= maxPendingPlayerEvents {
		p.app.log.Warn("dropping a player event while waiting on a load")
		return true
	}

	p.pendingPlayerEvents = append(p.pendingPlayerEvents, ev)
	return true
}

func (p *AppPlayer) drainPendingPlayerEvents() {
	pending := p.pendingPlayerEvents
	p.pendingPlayerEvents = nil

	for i := range pending {
		p.handlePlayerEvent(&pending[i])
	}
}

// forgetReplacedStreamEvents drops the held events that reported the outgoing
// stream ending. It kept playing while its replacement loaded, and if it ran
// out in the meantime its end is held here; handling that against the stream
// that just landed would advance straight past it.
func (p *AppPlayer) forgetReplacedStreamEvents() {
	p.pendingPlayerEvents = slices.DeleteFunc(p.pendingPlayerEvents, func(ev player.Event) bool {
		return ev.Type == player.EventTypeNotPlaying || ev.Type == player.EventTypeStop
	})
}

type loaderClass uint8

const (
	// classLoad is user-visible track and context work with an absolute
	// destination: a context, a chosen track, the track the pointer is on. A
	// new one cancels whatever is running and drops whatever is queued: only
	// the newest destination matters, and running the others first is exactly
	// the burst of skipping this lane exists to prevent.
	classLoad loaderClass = iota

	// classNav is relative navigation: one step forward or back from wherever
	// the pointer is. Dropping a step would change where a burst of them ends
	// up, so it is queued like a mutation; but the track being loaded or
	// prefetched is no longer the destination, so it cancels and drops those
	// like a load. The load it ends in is a classLoad of its own.
	classNav

	// classMutate changes the track list without loading any media. It is never
	// dropped and cancels nothing: losing a queued track would be visible.
	classMutate

	// classPrefetch is best effort and yields to everything else.
	classPrefetch
)

const (
	loaderQueueSize    = 32
	loadJobTimeout     = 60 * time.Second
	mutateJobTimeout   = 30 * time.Second
	prefetchJobTimeout = 30 * time.Second
	loaderDrainTimeout = 5 * time.Second
)

func (c loaderClass) timeout() time.Duration {
	switch c {
	case classMutate:
		return mutateJobTimeout
	case classPrefetch:
		return prefetchJobTimeout
	default:
		return loadJobTimeout
	}
}

// loaderJob is one unit of work owned by the loader goroutine. run executes off
// the player loop, so everything it touches must either belong to the job or be
// safe to use from anywhere.
type loaderJob struct {
	name  string
	class loaderClass
	gen   uint64
	reply replyTo
	run   func(ctx context.Context) loaderResult

	// notBefore holds the job in the queue until then. A load that waits there
	// can still be dropped by the next one without having cost anything, which
	// is how a burst of skips is made to pay for one load rather than each.
	notBefore time.Time
}

// loaderResult is what comes back to the player loop. commit performs the state
// writes and reports the outcome, and runs only while the result still describes
// what the daemon is trying to do; discard releases anything the job opened when
// it does not.
type loaderResult struct {
	name  string
	gen   uint64
	class loaderClass
	reply replyTo

	value   any
	err     error
	commit  func(p *AppPlayer, err error)
	discard func()
}

type runningJob struct {
	class  loaderClass
	cancel context.CancelFunc
}

// loaderLane runs one job at a time, off the player loop. Serialized rather than
// concurrent because the work it does is ordered — a load and the prefetch that
// follows it describe one decision about what plays next.
type loaderLane struct {
	log librespot.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	queue   []loaderJob
	running *runningJob
	closed  bool

	wake    chan struct{}
	results chan loaderResult
	done    chan struct{}
}

func newLoaderLane(log librespot.Logger) *loaderLane {
	ctx, cancel := context.WithCancel(context.Background())
	l := &loaderLane{
		log:     log,
		ctx:     ctx,
		cancel:  cancel,
		wake:    make(chan struct{}, 1),
		results: make(chan loaderResult, 4),
		done:    make(chan struct{}),
	}

	go l.run()
	return l
}

// submit queues a job, dropping or cancelling whatever the new one supersedes.
// Never blocks: it is called from the player loop.
func (l *loaderLane) submit(job loaderJob) {
	var dropped []loaderJob

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		job.reply.done(nil, ErrNoSession)
		return
	}

	switch job.class {
	case classLoad, classNav:
		dropped = l.dropLoadsLocked()
	case classPrefetch:
		if l.hasPendingLoad() {
			l.mu.Unlock()
			job.reply.done(nil, nil)
			return
		}
		dropped, l.queue = partitionQueue(l.queue, func(q loaderJob) bool {
			return q.class == classPrefetch
		})
		if l.running != nil && l.running.class == classPrefetch {
			l.running.cancel()
		}
	}

	if len(l.queue) >= loaderQueueSize {
		l.mu.Unlock()
		l.log.Warnf("dropping %s: the loader is too far behind", job.name)
		job.reply.done(nil, ErrLoaderBusy)
		replyAll(dropped, ErrSuperseded)
		return
	}

	l.queue = append(l.queue, job)
	l.mu.Unlock()

	replyAll(dropped, ErrSuperseded)

	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// dropLoadsLocked cancels the load or prefetch in flight and removes those
// queued, returning them so the caller can answer them once the lock is
// released. Mutations and navigation are left alone: they cancel nothing and
// are never dropped. Called with the lock held.
func (l *loaderLane) dropLoadsLocked() []loaderJob {
	var dropped []loaderJob
	dropped, l.queue = partitionQueue(l.queue, func(q loaderJob) bool {
		return q.class.fetches()
	})
	if l.running != nil && l.running.class.fetches() {
		l.running.cancel()
	}
	return dropped
}

// fetches reports whether a job of this class pulls media, which is the work
// worth abandoning once its destination changes.
func (c loaderClass) fetches() bool {
	return c == classLoad || c == classPrefetch
}

// loads reports whether a job of this class decides what plays next, and so
// makes fetching ahead of it pointless.
func (c loaderClass) loads() bool {
	return c == classLoad || c == classNav
}

// dropLoads abandons every load and prefetch, running or queued, for a caller
// that has stopped playback and wants nothing to land after it. Never blocks:
// it is called from the player loop.
func (l *loaderLane) dropLoads() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	dropped := l.dropLoadsLocked()
	l.mu.Unlock()

	replyAll(dropped, ErrSuperseded)
}

// hasPendingLoad reports whether a load, or the navigation that ends in one, is
// queued or running. Called with the lock held.
func (l *loaderLane) hasPendingLoad() bool {
	if l.running != nil && l.running.class.loads() {
		return true
	}

	for _, q := range l.queue {
		if q.class.loads() {
			return true
		}
	}
	return false
}

func replyAll(jobs []loaderJob, err error) {
	for _, job := range jobs {
		job.reply.done(nil, err)
	}
}

// partitionQueue splits jobs into those matching drop and those kept, preserving
// order in both.
func partitionQueue(jobs []loaderJob, drop func(loaderJob) bool) (dropped, kept []loaderJob) {
	for _, job := range jobs {
		if drop(job) {
			dropped = append(dropped, job)
		} else {
			kept = append(kept, job)
		}
	}
	return dropped, kept
}

// next takes the job at the head of the queue, or reports how long until it may
// be taken: a job whose time has not come stays queued, where a newer one can
// still drop it.
func (l *loaderLane) next() (loaderJob, context.Context, time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || len(l.queue) == 0 {
		return loaderJob{}, nil, 0, false
	}

	job := l.queue[0]
	if wait := time.Until(job.notBefore); wait > 0 {
		return loaderJob{}, nil, wait, false
	}
	l.queue = l.queue[1:]

	ctx, cancel := context.WithTimeout(l.ctx, job.class.timeout())
	l.running = &runningJob{class: job.class, cancel: cancel}

	return job, ctx, 0, true
}

func (l *loaderLane) execute(job loaderJob, ctx context.Context) loaderResult {
	defer func() {
		l.mu.Lock()
		if l.running != nil {
			l.running.cancel()
			l.running = nil
		}
		l.mu.Unlock()
	}()

	res := job.run(ctx)
	res.name = job.name
	res.gen = job.gen
	res.class = job.class
	res.reply = job.reply
	return res
}

func (l *loaderLane) run() {
	defer close(l.done)

	for {
		job, ctx, wait, ok := l.next()
		if !ok {
			// A nil channel never fires, so with nothing held back this only
			// wakes for a submit.
			var ready <-chan time.Time
			if wait > 0 {
				ready = time.After(wait)
			}

			select {
			case <-l.wake:
				continue
			case <-ready:
				continue
			case <-l.ctx.Done():
				return
			}
		}

		res := l.execute(job, ctx)

		select {
		case l.results <- res:
		case <-l.ctx.Done():
			if res.discard != nil {
				res.discard()
			}
			res.reply.done(nil, ErrNoSession)
			return
		}
	}
}

// close stops the lane, cancelling the job in flight and answering everything
// still queued. Bounded rather than waited out: a job stuck somewhere without a
// deadline must not hold up tearing the player down.
func (l *loaderLane) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	queued := l.queue
	l.queue = nil
	l.mu.Unlock()

	l.cancel()
	replyAll(queued, ErrNoSession)

	select {
	case <-l.done:
	case <-time.After(loaderDrainTimeout):
		l.log.Warn("loader lane did not drain in time")
	}
}
