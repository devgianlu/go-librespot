package daemon

import (
	"context"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/spclient"
)

// statePutTimeout bounds one push, including the retries spclient makes inside
// it. The player loop no longer waits on any of that, so this only has to stop
// a black-holed connection from pinning the lane.
const statePutTimeout = 20 * time.Second

// statePushQueueSize is small on purpose: consecutive player-state pushes
// coalesce, so a deep queue would only hold states nobody wants any more.
const statePushQueueSize = 4

// statePushDrainTimeout bounds how long Close waits for a push in flight.
const statePushDrainTimeout = 5 * time.Second

// statePush is one connect-state PUT, carrying bytes rather than the live
// protos the player loop keeps mutating.
type statePush struct {
	seq        uint64
	reason     connectpb.PutStateReason
	spotConnId string

	// body is the marshalled PutStateRequest, or nil for BECAME_INACTIVE, which
	// has an endpoint of its own and no payload.
	body []byte
}

// coalesces reports whether a push may be dropped in favour of a later one.
// Only the ordinary player-state pushes may: every other reason marks a
// transition the backend has to see in order.
func (p statePush) coalesces() bool {
	return p.reason == connectpb.PutStateReason_PLAYER_STATE_CHANGED
}

type statePushResult struct {
	seq      uint64
	reason   connectpb.PutStateReason
	publicIp string
	err      error
}

// statePushLane owns the connect-state PUT. It exists so that a stalled push
// cannot hold up the player loop, and — just as important — so that the push
// does not inherit whatever is left of a command handler's deadline: a slow
// track load used to leave the state push that followed it no time at all.
type statePushLane struct {
	log librespot.Logger
	put func(ctx context.Context, spotConnId string, reason connectpb.PutStateReason, body []byte) (*connectpb.Cluster, error)

	deviceId string

	mu     sync.Mutex
	queue  []statePush
	closed bool

	wake    chan struct{}
	results chan statePushResult
	quit    chan struct{}
	done    chan struct{}
}

func newStatePushLane(log librespot.Logger, sp *spclient.Spclient, deviceId string) *statePushLane {
	l := &statePushLane{
		log:      log,
		deviceId: deviceId,
		wake:     make(chan struct{}, 1),
		results:  make(chan statePushResult, statePushQueueSize),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	l.put = func(ctx context.Context, spotConnId string, reason connectpb.PutStateReason, body []byte) (*connectpb.Cluster, error) {
		if reason == connectpb.PutStateReason_BECAME_INACTIVE {
			return nil, sp.PutConnectStateInactive(ctx, spotConnId, false)
		}
		return sp.PutConnectStateRaw(ctx, spotConnId, reason, body)
	}

	go l.run()
	return l
}

// submit queues a push. Never blocks: it is called from the player loop.
func (l *statePushLane) submit(push statePush) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}

	if n := len(l.queue); n > 0 && l.queue[n-1].coalesces() && push.coalesces() {
		l.queue[n-1] = push
	} else {
		l.queue = append(l.queue, push)
	}

	// Overflow drops the oldest coalescable push rather than the newest: a
	// superseded player state is the one thing here worth nothing.
	for len(l.queue) > statePushQueueSize {
		dropped := -1
		for i, q := range l.queue {
			if q.coalesces() {
				dropped = i
				break
			}
		}
		if dropped < 0 {
			l.log.Warn("connect-state push queue is full of transitions, dropping the oldest")
			dropped = 0
		}
		l.queue = append(l.queue[:dropped], l.queue[dropped+1:]...)
	}
	l.mu.Unlock()

	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *statePushLane) next() (statePush, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.queue) == 0 {
		return statePush{}, false
	}

	push := l.queue[0]
	l.queue = l.queue[1:]
	return push, true
}

func (l *statePushLane) run() {
	defer close(l.done)

	for {
		push, ok := l.next()
		if !ok {
			select {
			case <-l.wake:
				continue
			case <-l.quit:
				return
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), statePutTimeout)
		go func() {
			select {
			case <-l.quit:
				cancel()
			case <-ctx.Done():
			}
		}()

		cluster, err := l.put(ctx, push.spotConnId, push.reason, push.body)
		cancel()

		res := statePushResult{seq: push.seq, reason: push.reason, err: err}
		if device := cluster.GetDevice()[l.deviceId]; device != nil {
			res.publicIp = device.PublicIp
		}

		select {
		case l.results <- res:
		case <-l.quit:
			return
		}
	}
}

// close stops the lane, abandoning anything queued. A push in flight is
// cancelled rather than waited out: the session it described is going away.
func (l *statePushLane) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.queue = nil
	l.mu.Unlock()

	close(l.quit)

	select {
	case <-l.done:
	case <-time.After(statePushDrainTimeout):
		l.log.Warn("connect-state push lane did not drain in time")
	}
}
