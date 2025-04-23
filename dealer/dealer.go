package dealer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
	"nhooyr.io/websocket"
)

const (
	pingInterval = 30 * time.Second
	timeout      = 10 * time.Second
)

type Dealer struct {
	log librespot.Logger

	client *http.Client

	addr        librespot.GetAddressFunc
	accessToken librespot.GetLogin5TokenFunc

	conn *websocket.Conn

	stop           bool
	pingTickerStop chan struct{}
	recvLoopStop   chan struct{}
	recvLoopOnce   sync.Once
	lastPong       time.Time
	lastPongLock   sync.Mutex

	// connMu is held for writing when performing reconnection and for reading when accessing the conn.
	// If it's not held, a valid connection is available. Be careful not to deadlock anything with this.
	connMu sync.RWMutex

	messageReceivers     []messageReceiver
	messageReceiversLock sync.RWMutex

	requestReceivers     map[string]requestReceiver
	requestReceiversLock sync.RWMutex
}

func NewDealer(log librespot.Logger, client *http.Client, dealerAddr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc) *Dealer {
	return &Dealer{
		client: &http.Client{
			Transport:     client.Transport,
			CheckRedirect: client.CheckRedirect,
			Jar:           client.Jar,
			Timeout:       timeout,
		},
		log:              log,
		addr:             dealerAddr,
		accessToken:      accessToken,
		requestReceivers: map[string]requestReceiver{},
	}
}

func (d *Dealer) Connect(ctx context.Context) error {
	d.connMu.Lock()
	defer d.connMu.Unlock()

	if d.conn != nil && !d.stop {
		d.log.Debugf("dealer connection already opened")
		return nil
	}

	return d.connect(ctx)
}

func (d *Dealer) connect(ctx context.Context) error {
	d.recvLoopStop = make(chan struct{}, 1)
	d.pingTickerStop = make(chan struct{}, 1)
	d.stop = false

	accessToken, err := d.accessToken(ctx, false)
	if err != nil {
		return fmt.Errorf("failed obtaining dealer access token: %w", err)
	}

	if conn, _, err := websocket.Dial(context.Background(), fmt.Sprintf("wss://%s/?access_token=%s", d.addr(ctx), accessToken), &websocket.DialOptions{
		HTTPClient: d.client,
		HTTPHeader: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
		},
	}); err != nil {
		return err
	} else {
		// we assign to d.conn after because if Dial fails we'll have a nil d.conn which we don't want
		d.conn = conn
	}

	// remove the read limit
	d.conn.SetReadLimit(math.MaxUint32)

	d.log.Debugf("dealer connection opened")

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
		d.log.Tracef("starting dealer recv loop")
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
			d.lastPongLock.Lock()
			timePassed := time.Since(d.lastPong)
			d.lastPongLock.Unlock()
			if timePassed > pingInterval+timeout {
				d.log.Errorf("did not receive last pong from dealer, %.0fs passed", timePassed.Seconds())

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
			d.log.Tracef("sent dealer ping")

			if err != nil {
				if d.stop {
					// break early without logging if we should stop
					break loop
				}

				d.log.WithError(err).Warnf("failed sending dealer ping")

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
				d.log.Debugf("dealer connection closed")
				break loop
			} else if err != nil {
				d.log.WithError(err).Errorf("failed receiving dealer message")
				break loop
			} else if msgType != websocket.MessageText {
				d.log.WithError(err).Warnf("unsupported message type: %v, len: %d", msgType, len(messageBytes))
				continue
			}

			var message RawMessage
			if err := json.Unmarshal(messageBytes, &message); err != nil {
				d.log.WithError(err).Error("failed unmarshalling dealer message")
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
				d.lastPongLock.Lock()
				d.lastPong = time.Now()
				d.lastPongLock.Unlock()
				d.log.Tracef("received dealer pong")
				break
			default:
				d.log.Warnf("unknown dealer message type: %s", message.Type)
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
			d.log.WithError(err).Errorf("failed reconnecting dealer")
			d.connMu.Unlock()

			// something went very wrong, give up
			d.Close()
		} else {
			d.connMu.Unlock()

			// reconnection was successful, do not close receivers
			return
		}
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

	d.log.Debugf("dealer recv loop stopped")
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
	if err := d.connect(context.TODO()); err != nil {
		return err
	}

	d.lastPongLock.Lock()
	d.lastPong = time.Now()
	d.lastPongLock.Unlock()
	// restart the recv loop
	go d.recvLoop()

	d.log.Debugf("re-established dealer connection")
	return nil
}
