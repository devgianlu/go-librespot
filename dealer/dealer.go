package dealer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
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

	// reconnectLock is held for writing when performing reconnection and for reading when accessing the conn.
	// If it's not held, a valid connection is available. Be careful not to deadlock anything with this.
	reconnectLock sync.RWMutex

	messageReceivers     []messageReceiver
	messageReceiversLock sync.RWMutex

	requestReceivers     map[string]requestReceiver
	requestReceiversLock sync.RWMutex
}

func NewDealer(dealerAddr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc) (*Dealer, error) {
	dealer := &Dealer{addr: dealerAddr, accessToken: accessToken}
	dealer.requestReceivers = map[string]requestReceiver{}
	dealer.recvLoopStop = make(chan struct{}, 1)
	dealer.pingTickerStop = make(chan struct{}, 1)

	// open connection to dealer
	if err := dealer.connect(); err != nil {
		return nil, err
	}

	// start the ping ticker, this should stop only if we close the dealer definitely
	go dealer.pingTicker()

	log.Debugf("dealer connection opened")
	return dealer, nil
}

func (d *Dealer) connect() error {
	accessToken, err := d.accessToken()
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

	// set last pong in the future
	d.lastPong = time.Now().Add(pingInterval)
	return nil
}

func (d *Dealer) Close() {
	d.stop = true
	d.recvLoopStop <- struct{}{}
	d.pingTickerStop <- struct{}{}
	_ = d.conn.Close(websocket.StatusGoingAway, "")
}

func (d *Dealer) startReceiving() {
	d.recvLoopOnce.Do(func() { go d.recvLoop() })
}

func (d *Dealer) pingTicker() {
	ticker := time.NewTicker(pingInterval)

loop:
	for {
		select {
		case <-d.pingTickerStop:
			break loop
		case <-ticker.C:
			if time.Since(d.lastPong) > pingInterval {
				log.Errorf("did not receive last pong from dealer, %.0fs passed", time.Since(d.lastPong).Seconds())

				// closing the connection should make the read on the "recvLoop" fail,
				// continue hoping for a new connection
				_ = d.conn.Close(websocket.StatusServiceRestart, "")
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			d.reconnectLock.RLock()
			err := d.conn.Write(ctx, websocket.MessageText, []byte("{\"type\":\"ping\"}"))
			d.reconnectLock.RUnlock()
			cancel()

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
			// no need to hold the reconnectLock since reconnection happens in this routine
			msgType, messageBytes, err := d.conn.Read(context.Background())
			if err != nil {
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
				break
			default:
				log.Warnf("unknown dealer message type: %s", message.Type)
				break
			}
		}
	}

	_ = d.conn.Close(websocket.StatusInternalError, "")

	// if we shouldn't stop, try to reconnect
	if !d.stop {
		d.reconnectLock.Lock()
		if err := backoff.Retry(d.reconnect, backoff.NewExponentialBackOff()); err != nil {
			log.WithError(err).Errorf("failed reconnecting dealer, bye bye")
			log.Exit(1)
		}
		d.reconnectLock.Unlock()

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
}

func (d *Dealer) sendReply(key string, success bool) error {
	reply := Reply{Type: "reply", Key: key}
	reply.Payload.Success = success

	replyBytes, err := json.Marshal(reply)
	if err != nil {
		return fmt.Errorf("failed marshalling reply: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	d.reconnectLock.RLock()
	err = d.conn.Write(ctx, websocket.MessageText, replyBytes)
	d.reconnectLock.RUnlock()
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

	// restart the recv loop
	go d.recvLoop()

	log.Debugf("re-established dealer connection")
	return nil
}
