package dealer

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	librespot "github.com/devgianlu/go-librespot"
)

func TestPingTickerDoesNotPanicWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &Dealer{
			log:            &librespot.NullLogger{},
			pingTickerStop: make(chan struct{}, 1),
		}

		panicCh := make(chan any, 1)
		go func() {
			defer func() {
				panicCh <- recover()
			}()
			d.pingTicker()
		}()

		time.Sleep(pingInterval + timeout + time.Nanosecond)
		synctest.Wait()

		select {
		case p := <-panicCh:
			if p != nil {
				t.Fatalf("pingTicker panicked when conn was nil: %v", p)
			}
		default:
		}

		d.pingTickerStop <- struct{}{}
		synctest.Wait()

		select {
		case p := <-panicCh:
			if p != nil {
				t.Fatalf("pingTicker panicked when conn was nil: %v", p)
			}
		default:
			t.Fatal("pingTicker did not stop")
		}
	})
}

func TestCloseStopsPingTickerWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &Dealer{
			log:            &librespot.NullLogger{},
			pingTickerStop: make(chan struct{}, 1),
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.pingTicker()
		}()

		synctest.Wait()
		d.Close()
		synctest.Wait()

		stopped := false
		select {
		case <-done:
			stopped = true
		default:
		}

		d.pingTickerStop <- struct{}{}
		synctest.Wait()

		if !stopped {
			t.Fatal("pingTicker did not stop when closing with nil conn")
		}
	})
}

func TestWriteConnRejectsClosedDealer(t *testing.T) {
	d := &Dealer{closed: true}

	_, err := d.writeConn(context.Background(), websocket.MessageText, nil)
	if !errors.Is(err, ErrDealerClosed) {
		t.Fatalf("expected ErrDealerClosed, got %v", err)
	}
}
