//go:build test_unit

package ap

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
)

type stubAddr string

func (a stubAddr) Network() string { return string(a) }
func (a stubAddr) String() string  { return string(a) }

type blockingConn struct {
	startCh  chan struct{}
	closedCh chan struct{}
}

func newBlockingConn() *blockingConn {
	return &blockingConn{
		startCh:  make(chan struct{}),
		closedCh: make(chan struct{}),
	}
}

func (c *blockingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *blockingConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *blockingConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *blockingConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingConn) SetWriteDeadline(time.Time) error { return nil }

func (c *blockingConn) Write([]byte) (int, error) {
	close(c.startCh)
	<-c.closedCh
	return 0, io.ErrClosedPipe
}

func (c *blockingConn) Close() error {
	close(c.closedCh)
	return nil
}

func TestPongAckTickerStopsWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ap := NewAccesspoint(&librespot.NullLogger{}, nil, "")
		stopped := make(chan struct{})

		go func() {
			defer close(stopped)
			ap.pongAckTicker()
		}()

		time.Sleep(pongAckInterval + time.Nanosecond)
		synctest.Wait()

		ap.Close()
		synctest.Wait()

		select {
		case <-stopped:
		default:
			t.Fatal("pongAckTicker did not stop")
		}
	})
}

func TestDoneChannelClosesOnClose(t *testing.T) {
	ap := NewAccesspoint(&librespot.NullLogger{}, nil, "")

	select {
	case <-ap.Done():
		t.Fatal("done channel should remain open before Close")
	default:
	}

	ap.Close()

	select {
	case <-ap.Done():
	default:
		t.Fatal("done channel did not close")
	}

	ap.Close()

	select {
	case <-ap.Done():
	default:
		t.Fatal("done channel should stay closed after repeated Close")
	}
}

func TestCloseSignalsAndUnblocksInFlightSend(t *testing.T) {
	conn := newBlockingConn()
	ap := NewAccesspoint(&librespot.NullLogger{}, nil, "")
	ap.conn = conn
	ap.encConn = newShannonConn(conn, make([]byte, 32), make([]byte, 32))

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- ap.Send(context.Background(), PacketTypePing, []byte("payload"))
	}()

	select {
	case <-conn.startCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for send to start")
	}

	closeDone := make(chan struct{})
	go func() {
		ap.Close()
		close(closeDone)
	}()

	select {
	case <-ap.Done():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for done channel to close")
	}

	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for close to finish")
	}

	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrAccesspointClosed) {
			t.Fatalf("expected ErrAccesspointClosed, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for send to finish")
	}
}

// A reconnect waiting out its backoff has to give up as soon as the
// accesspoint is closed. Without a context on the retry it keeps going for the
// backoff's own budget — 15 minutes by default — while holding connMu, which
// stalls shutdown and blocks every Send behind it.
func TestCloseCancelsReconnectBackoff(t *testing.T) {
	ap := NewAccesspoint(&librespot.NullLogger{}, nil, "")

	attempted := make(chan struct{}, 1)
	retryDone := make(chan error, 1)
	go func() {
		retryDone <- backoff.Retry(func() error {
			select {
			case attempted <- struct{}{}:
			default:
			}
			return errors.New("accesspoint still unreachable")
		}, backoff.WithContext(backoff.NewExponentialBackOff(), ap.ctx))
	}()

	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("retry never ran")
	}

	ap.Close()

	select {
	case err := <-retryDone:
		if err == nil {
			t.Fatal("expected the cancelled retry to report an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect backoff kept running after Close")
	}
}
