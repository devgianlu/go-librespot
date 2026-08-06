//go:build test_unit

package dealer

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/coder/websocket"
	librespot "github.com/devgianlu/go-librespot"
)

func TestPingTickerStopsWhenConnNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		d := &Dealer{
			log:    &librespot.NullLogger{},
			ctx:    ctx,
			cancel: cancel,
			done:   make(chan struct{}),
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
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dealer{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	close(d.done)

	_, err := d.writeConn(context.Background(), websocket.MessageText, nil)
	if !errors.Is(err, ErrDealerClosed) {
		t.Fatalf("expected ErrDealerClosed, got %v", err)
	}
}

// Same as the accesspoint: a dealer reconnect sitting in its backoff must stop
// when the dealer is closed rather than retrying for the backoff's own budget.
func TestCloseCancelsReconnectBackoff(t *testing.T) {
	d := NewDealer(&librespot.NullLogger{}, &http.Client{}, nil, nil)
	d.log = &librespot.NullLogger{}

	attempted := make(chan struct{}, 1)
	retryDone := make(chan error, 1)
	go func() {
		retryDone <- backoff.Retry(func() error {
			select {
			case attempted <- struct{}{}:
			default:
			}
			return errors.New("dealer still unreachable")
		}, backoff.WithContext(backoff.NewExponentialBackOff(), d.ctx))
	}()

	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("retry never ran")
	}

	d.Close()

	select {
	case err := <-retryDone:
		if err == nil {
			t.Fatal("expected the cancelled retry to report an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect backoff kept running after Close")
	}
}
