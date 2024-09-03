package dealer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
	"math"
	"net/http"
	"nhooyr.io/websocket"
	"sync"
	"time"
)

const (
	pingInterval = 30 * time.Second
	timeout      = 10 * time.Second
)

type Dealer struct {
	addr        librespot.GetAddressFunc
	accessToken librespot.GetLogin5TokenFunc

	conn *websocket.Conn

	stop           bool
	pingTickerStop chan struct{}
	recvLoopStop   chan struct{}
	recvLoopOnce   sync.Once
	lastPong       time.Time

	// connMu is held for writing when performing reconnection and for reading when accessing the conn.
	// If it's not held, a valid connection is available. Be careful not to deadlock anything with this.
	connMu sync.RWMutex

	messageReceivers     []messageReceiver
	messageReceiversLock sync.RWMutex

	requestReceivers     map[string]requestReceiver
	requestReceiversLock sync.RWMutex
}

func NewDealer(dealerAddr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc) *Dealer {
	return &Dealer{addr: dealerAddr, accessToken: accessToken, requestReceivers: map[string]requestReceiver{}}
}

func (d *Dealer) Connect() error {
	d.connMu.Lock()
	defer d.connMu.Unlock()

	if d.conn != nil && !d.stop {
		log.Debugf("dealer connection already opened")
		return nil
	}

	return d.connect()
}

func (d *Dealer) connect() error {
	d.recvLoopStop = make(chan struct{}, 1)
	d.pingTickerStop = make(chan struct{}, 1)
	d.stop = false

	accessToken, err := d.accessToken(false)
	if err != nil {
		return fmt.Errorf("failed obtaining dealer access token: %w", err)
	}

	if conn, _, err := websocket.Dial(context.Background(), fmt.Sprintf("wss://%s/?access_token=%s", d.addr(), accessToken), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
		},
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
	}); err != nil {
		return err
	} else {
		// we assign to d.conn after because if Dial fails we'll have a nil d.conn which we don't want
		d.conn = conn
	}

	// remove the read limit
	d.conn.SetReadLimit(math.MaxUint32)

	log.Debugf("dealer connection opened")

	return nil
}

func (d *Dealer) Close() {
	d.connMu.Lock()
	defer d.connMu.Unlock()

	d.stop = true

	if d.conn == nil {
		return
	}

	d.recvLoopStop <- struct{}{}
	d.pingTickerStop <- struct{}{}
	_ = d.conn.Close(websocket.StatusGoingAway, "")
}

func (d *Dealer) startReceiving() {
	d.recvLoopOnce.Do(func() {
		log.Tracef("starting dealer recv loop")
		go d.recvLoop()

		// set last pong in the future
		d.lastPong = time.Now().Add(pingInterval)
		go d.pingTicker()
	})
}

func (d *Dealer) pingTicker() {
	ticker := time.NewTicker(pingInterval)

loop:
	for {
		select {
		case <-d.pingTickerStop:
			break loop
		case <-ticker.C:
			if time.Since(d.lastPong) > pingInterval+timeout {
				log.Errorf("did not receive last pong from dealer, %.0fs passed", time.Since(d.lastPong).Seconds())

				// closing the connection should make the read on the "recvLoop" fail,
				// continue hoping for a new connection
				_ = d.conn.Close(websocket.StatusServiceRestart, "")
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			d.connMu.RLock()
			err := d.conn.Write(ctx, websocket.MessageText, []byte("{\"type\":\"ping\"}"))
			d.connMu.RUnlock()
			cancel()
			log.Tracef("sent dealer ping")

			if err != nil {
				if d.stop {
					// break early without logging if we should stop
					break loop
				}

				log.WithError(err).Warnf("failed sending dealer ping")

				// closing the connection should make the read on the "recvLoop" fail,
				// continue hoping for a new connection
				_ = d.conn.Close(websocket.StatusServiceRestart, "")
				continue
			}
		}
	}

	ticker.Stop()
}

func (d *Dealer) recvLoop() {
loop:
	for {
		select {
		case <-d.recvLoopStop:
			break loop
		default:
			// no need to hold the connMu since reconnection happens in this routine
			msgType, messageBytes, err := d.conn.Read(context.Background())

			// don't log closed error if we're stopping
			if d.stop && websocket.CloseStatus(err) == websocket.StatusGoingAway {
				log.Debugf("dealer connection closed")
				break loop
			} else if err != nil {
				log.WithError(err).Errorf("failed receiving dealer message")
				break loop
			} else if msgType != websocket.MessageText {
				log.WithError(err).Warnf("unsupported message type: %v, len: %d", msgType, len(messageBytes))
				continue
			}

			var message RawMessage
			if err := json.Unmarshal(messageBytes, &message); err != nil {
				log.WithError(err).Error("failed unmarshalling dealer message")
				break loop
			}

			switch message.Type {
			case "message":
				d.handleMessage(&message)
				break
			case "request":
				d.handleRequest(&message)
				break
			case "ping":
				// we never receive ping messages
				break
			case "pong":
				d.lastPong = time.Now()
				log.Tracef("received dealer pong")
				break
			default:
				log.Warnf("unknown dealer message type: %s", message.Type)
				break
			}
		}
	}

	// always close as we might end up here because of application errors
	_ = d.conn.Close(websocket.StatusInternalError, "")

	// if we shouldn't stop, try to reconnect
	if !d.stop {
		d.connMu.Lock()
		if err := backoff.Retry(d.reconnect, backoff.NewExponentialBackOff()); err != nil {
			log.WithError(err).Errorf("failed reconnecting dealer, bye bye")
			log.Exit(1)
		}
		d.connMu.Unlock()

		// reconnection was successful, do not close receivers
		return
	}

	d.requestReceiversLock.RLock()
	for _, recv := range d.requestReceivers {
		close(recv.c)
	}
	d.requestReceiversLock.RUnlock()

	d.messageReceiversLock.RLock()
	for _, recv := range d.messageReceivers {
		close(recv.c)
	}
	d.messageReceiversLock.RUnlock()

	log.Debugf("dealer recv loop stopped")
}

func (d *Dealer) sendReply(key string, success bool) error {
	reply := Reply{Type: "reply", Key: key}
	reply.Payload.Success = success

	replyBytes, err := json.Marshal(reply)
	if err != nil {
		return fmt.Errorf("failed marshalling reply: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	d.connMu.RLock()
	err = d.conn.Write(ctx, websocket.MessageText, replyBytes)
	d.connMu.RUnlock()
	cancel()
	if err != nil {
		return fmt.Errorf("failed sending dealer reply: %w", err)
	}

	return nil
}

func (d *Dealer) reconnect() error {
	if err := d.connect(); err != nil {
		return err
	}

	d.lastPong = time.Now()
	// restart the recv loop
	go d.recvLoop()

	log.Debugf("re-established dealer connection")
	return nil
}
