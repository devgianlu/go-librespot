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

func TestPingTickerStopsWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &Dealer{
			log:  &librespot.NullLogger{},
			done: make(chan struct{}),
		}
		stopped := make(chan struct{})

		go func() {
			defer close(stopped)
			d.pingTicker()
		}()

		time.Sleep(pingInterval + time.Nanosecond)
		synctest.Wait()

		d.Close()
		synctest.Wait()

		select {
		case <-stopped:
		default:
			t.Fatal("pingTicker did not stop")
		}
	})
}

func TestWriteConnRejectsClosedDealer(t *testing.T) {
	d := &Dealer{done: make(chan struct{})}
	close(d.done)

	_, err := d.writeConn(context.Background(), websocket.MessageText, nil)
	if !errors.Is(err, ErrDealerClosed) {
		t.Fatalf("expected ErrDealerClosed, got %v", err)
	}
}
