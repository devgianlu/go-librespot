package ap

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	librespot "github.com/devgianlu/go-librespot"
)

type stubAddr string

func (a stubAddr) Network() string { return string(a) }
func (a stubAddr) String() string  { return string(a) }

type countingConn struct {
	mu     sync.Mutex
	closes int
}

func (c *countingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *countingConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *countingConn) Close() error                     { c.mu.Lock(); c.closes++; c.mu.Unlock(); return nil }
func (c *countingConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *countingConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *countingConn) SetDeadline(time.Time) error      { return nil }
func (c *countingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *countingConn) SetWriteDeadline(time.Time) error { return nil }

func (c *countingConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

type blockingConn struct {
	started sync.Once
	startCh chan struct{}
	blockCh chan struct{}
}

func newBlockingConn() *blockingConn {
	return &blockingConn{
		startCh: make(chan struct{}),
		blockCh: make(chan struct{}),
	}
}

func (c *blockingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *blockingConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *blockingConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *blockingConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingConn) SetWriteDeadline(time.Time) error { return nil }

func (c *blockingConn) Write(b []byte) (int, error) {
	c.started.Do(func() { close(c.startCh) })
	<-c.blockCh
	return len(b), nil
}

func (c *blockingConn) Close() error {
	return nil
}

func TestPongAckTickerDoesNotPanicWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ap := NewAccesspoint(&librespot.NullLogger{}, nil, "")

		panicCh := make(chan any, 1)
		go func() {
			defer func() {
				panicCh <- recover()
			}()
			ap.pongAckTicker()
		}()

		time.Sleep(pongAckInterval + time.Nanosecond)
		synctest.Wait()

		select {
		case p := <-panicCh:
			if p != nil {
				t.Fatalf("pongAckTicker panicked when conn was nil: %v", p)
			}
		default:
		}

		ap.pongAckTickerStop <- struct{}{}
		synctest.Wait()

		select {
		case p := <-panicCh:
			if p != nil {
				t.Fatalf("pongAckTicker panicked when conn was nil: %v", p)
			}
		default:
			t.Fatal("pongAckTicker did not stop")
		}
	})
}

func TestCloseStopsPongAckTickerWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ap := NewAccesspoint(&librespot.NullLogger{}, nil, "")
		done := make(chan struct{})

		go func() {
			defer close(done)
			ap.pongAckTicker()
		}()

		synctest.Wait()
		ap.Close()
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Fatal("pongAckTicker did not stop when closing with nil conn")
		}
	})
}

func TestCloseWaitsForInFlightSend(t *testing.T) {
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
	case <-closeDone:
		t.Fatal("close returned before in-flight send finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(conn.blockCh)

	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("send error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for send to finish")
	}

	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for close to finish")
	}
}
