//go:build test_unit

package dealer

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	librespot "github.com/devgianlu/go-librespot"
)

// cuttableConn drops everything read and written once cut, like a connection
// whose network path went away without a FIN or an RST.
type cuttableConn struct {
	net.Conn
	cut *atomic.Bool
}

func (c *cuttableConn) Read(b []byte) (int, error) {
	for {
		n, err := c.Conn.Read(b)
		if err != nil || !c.cut.Load() {
			return n, err
		}
	}
}

func (c *cuttableConn) Write(b []byte) (int, error) {
	if c.cut.Load() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

// pipeListener hands the server ends of in-memory connections to an http.Server.
type pipeListener struct {
	conns     chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "pipe"} }

// fakeDealer is a dealer endpoint that answers every ping with a pong after
// pongDelay, reached over in-memory connections that the test can cut.
type fakeDealer struct {
	pongDelay time.Duration

	// requestAt, when set, is when the first connection pushes one player
	// command to the device. replies counts the replies that came back.
	requestAt time.Duration
	requested atomic.Bool
	replies   atomic.Int32

	listener  *pipeListener
	server    *http.Server
	transport *http.Transport
	served    chan struct{}
	handlers  sync.WaitGroup

	mu    sync.Mutex
	dials []time.Time
	cut   *atomic.Bool
}

func newFakeDealer(pongDelay time.Duration) *fakeDealer {
	f := &fakeDealer{
		pongDelay: pongDelay,
		listener:  &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})},
		served:    make(chan struct{}),
	}
	f.server = &http.Server{Handler: http.HandlerFunc(f.serveWebsocket)}
	f.transport = &http.Transport{
		// The returned conn counts as already past its TLS handshake.
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			client, server := net.Pipe()
			cut := &atomic.Bool{}

			f.mu.Lock()
			f.dials = append(f.dials, time.Now())
			f.cut = cut
			f.mu.Unlock()

			select {
			case f.listener.conns <- server:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &cuttableConn{Conn: client, cut: cut}, nil
		},
		TLSClientConfig: &tls.Config{},
	}
	go func() {
		defer close(f.served)
		_ = f.server.Serve(f.listener)
	}()
	return f
}

func (f *fakeDealer) serveWebsocket(w http.ResponseWriter, r *http.Request) {
	f.handlers.Add(1)
	defer f.handlers.Done()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	if f.requestAt > 0 && f.requested.CompareAndSwap(false, true) {
		f.handlers.Add(1)
		go func() {
			defer f.handlers.Done()
			time.Sleep(f.requestAt)
			payload := base64.StdEncoding.EncodeToString([]byte(`{"command":{"endpoint":"transfer"}}`))
			msg := fmt.Sprintf(`{"type":"request","message_ident":%q,"key":"k1","payload":{"compressed":%q}}`, playerCommandUri, payload)
			_ = conn.Write(context.Background(), websocket.MessageText, []byte(msg))
		}()
	}

	for {
		_, msg, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if string(msg) != `{"type":"ping"}` {
			f.replies.Add(1)
			continue
		}
		// net.Pipe has no buffer: answer from another goroutine so this loop
		// keeps reading while the device is not, as a socket buffer would.
		f.handlers.Add(1)
		go func() {
			defer f.handlers.Done()
			time.Sleep(f.pongDelay)
			_ = conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"pong"}`))
		}()
	}
}

const playerCommandUri = "hm://connect-state/v1/player/command"

// handleCommands answers every player command after handleFor, as the
// player's main loop does once it has processed the command.
func handleCommands(d *Dealer, handleFor time.Duration) {
	reqs := d.ReceiveRequest(playerCommandUri)
	go func() {
		for req := range reqs {
			time.Sleep(handleFor)
			req.Reply(true)
		}
	}()
}

func (f *fakeDealer) dealer() *Dealer {
	return NewDealer(
		&librespot.NullLogger{},
		&http.Client{Transport: f.transport},
		func(context.Context) string { return "dealer.test:443" },
		func(context.Context, bool) (string, error) { return "token", nil },
	)
}

// cutCurrent makes the latest connection go silent in both directions.
func (f *fakeDealer) cutCurrent() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cut.Store(true)
}

func (f *fakeDealer) dialTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.dials...)
}

func (f *fakeDealer) close() {
	_ = f.server.Close()
	<-f.served
	// A hijacked connection outlives the server: wait for each handler to
	// see its connection closed, and to finish a pong it was delaying.
	f.handlers.Wait()
	f.transport.CloseIdleConnections()
}

// runDealer connects a dealer to f and returns what stops both. The caller
// defers it: the bubble refuses to end while a goroutine is still blocked, and
// a deferred call also runs after t.Fatal.
func runDealer(t *testing.T, f *fakeDealer) (stop func()) {
	stop, _ = runDealerWith(t, f)
	return stop
}

func runDealerWith(t *testing.T, f *fakeDealer) (stop func(), d *Dealer) {
	d = f.dealer()
	stop = func() {
		d.Close()
		f.close()
		synctest.Wait()
	}
	if err := d.Connect(context.Background()); err != nil {
		stop()
		t.Fatalf("connect: %v", err)
	}
	d.startReceiving()
	return stop, d
}

// A command sent from the phone travels on the dealer connection, so a dead
// connection must be noticed and replaced quickly: everything sent until then
// is lost. The cut lands just after a pong, the worst case for detection.
func TestDealerReplacesSilentConnectionWithin25s(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeDealer(0)
		defer runDealer(t, f)()

		// Two minutes is a whole number of ping periods: a ping has just
		// been sent and answered.
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		f.cutCurrent()
		cutAt := time.Now()

		time.Sleep(2 * time.Minute)
		synctest.Wait()

		dials := f.dialTimes()
		if len(dials) < 2 {
			t.Fatalf("the silent connection was never replaced (%d dial)", len(dials))
		}
		if took := dials[1].Sub(cutAt); took > 25*time.Second {
			t.Fatalf("the silent connection was replaced %s after the cut, want at most 25s", took)
		}
	})
}

// A slow link is not a dead one: a pong that takes seconds to come back must
// not cost the connection.
func TestDealerKeepsSlowConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeDealer(5 * time.Second)
		defer runDealer(t, f)()

		time.Sleep(10 * time.Minute)
		synctest.Wait()

		if dials := f.dialTimes(); len(dials) != 1 {
			t.Fatalf("a slow but answering connection was replaced: %d dials", len(dials))
		}
	})
}

// The dealer is read by one loop, which reads nothing while the player
// processes a command: a transfer loads the context and the track over
// spclient, whose requests may take up to 30 s. A pong waiting unread behind
// that command must not cost the connection, nor the command its reply.
func TestDealerKeepsConnectionWhileCommandIsSlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeDealer(0)
		f.requestAt = time.Second
		stop, d := runDealerWith(t, f)
		defer stop()
		handleCommands(d, 25*time.Second)

		time.Sleep(3 * time.Minute)
		synctest.Wait()

		if dials := f.dialTimes(); len(dials) != 1 {
			t.Fatalf("a live connection was replaced while a command was processed: %d dials", len(dials))
		}
		if replies := f.replies.Load(); replies != 1 {
			t.Fatalf("the slow command's reply was lost: %d replies", replies)
		}
	})
}

// Waiting for a slow command must not hide a dead connection: once the
// command is answered, a connection that went silent meanwhile is replaced
// within one ping period and one pong timeout.
func TestDealerReplacesSilentConnectionAfterSlowCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeDealer(0)
		f.requestAt = time.Second
		stop, d := runDealerWith(t, f)
		defer stop()
		const handleFor = 25 * time.Second
		handleCommands(d, handleFor)

		time.Sleep(2 * time.Second)
		synctest.Wait()
		f.cutCurrent()
		answeredAt := time.Now().Add(f.requestAt + handleFor - 2*time.Second)

		time.Sleep(3 * time.Minute)
		synctest.Wait()

		dials := f.dialTimes()
		if len(dials) < 2 {
			t.Fatalf("the silent connection was never replaced (%d dial)", len(dials))
		}
		if took := dials[1].Sub(answeredAt); took > 25*time.Second {
			t.Fatalf("the silent connection was replaced %s after the command was answered, want at most 25s", took)
		}
	})
}
