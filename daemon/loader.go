package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
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
		defer p.drainPendingPlayerEvents()
	}

	if res.gen != p.loadGen {
		// A newer command has already changed what the daemon is trying to do,
		// so this result describes a decision nobody is waiting for any more.
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
		p.handlePlayerEvent(p.ctx, &pending[i])
	}
}

type loaderClass uint8

const (
	// classLoad is user-visible track and context work. A new one cancels
	// whatever is running and drops whatever is queued: only the newest
	// destination matters, and running the others first is exactly the burst of
	// skipping this lane exists to prevent.
	classLoad loaderClass = iota

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
	case classLoad:
		dropped, l.queue = partitionQueue(l.queue, func(q loaderJob) bool {
			return q.class == classLoad || q.class == classPrefetch
		})
		if l.running != nil && l.running.class != classMutate {
			l.running.cancel()
		}
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

// hasPendingLoad reports whether a load is queued or running. Called with the
// lock held.
func (l *loaderLane) hasPendingLoad() bool {
	if l.running != nil && l.running.class == classLoad {
		return true
	}

	for _, q := range l.queue {
		if q.class == classLoad {
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

func (l *loaderLane) next() (loaderJob, context.Context, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || len(l.queue) == 0 {
		return loaderJob{}, nil, false
	}

	job := l.queue[0]
	l.queue = l.queue[1:]

	ctx, cancel := context.WithTimeout(l.ctx, job.class.timeout())
	l.running = &runningJob{class: job.class, cancel: cancel}

	return job, ctx, true
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
		job, ctx, ok := l.next()
		if !ok {
			select {
			case <-l.wake:
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
