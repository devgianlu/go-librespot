package dealer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/coder/websocket"
	librespot "github.com/devgianlu/go-librespot"
)

const (
	pingInterval = 30 * time.Second
	timeout      = 10 * time.Second
)

var ErrDealerClosed = errors.New("dealer closed")

type Dealer struct {
	log librespot.Logger

	client *http.Client

	addr        librespot.GetAddressFunc
	accessToken librespot.GetLogin5TokenFunc

	conn *websocket.Conn

	closed         bool
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
		pingTickerStop:   make(chan struct{}, 1),
		recvLoopStop:     make(chan struct{}, 1),
		requestReceivers: map[string]requestReceiver{},
	}
}

func (d *Dealer) Connect(ctx context.Context) error {
	d.connMu.Lock()
	defer d.connMu.Unlock()

	if d.closed {
		return ErrDealerClosed
	}

	if d.conn != nil && !d.stop {
		d.log.Debugf("dealer connection already opened")
		return nil
	}

	return d.connect(ctx)
}

func (d *Dealer) connect(ctx context.Context) error {
	d.stop = false

	accessToken, err := d.accessToken(ctx, false)
	if err != nil {
		return fmt.Errorf("failed obtaining dealer access token: %w", err)
	}

	addr := d.addr(ctx)
	if conn, _, err := websocket.Dial(ctx, fmt.Sprintf("wss://%s/?access_token=%s", addr, accessToken), &websocket.DialOptions{
		HTTPClient: d.client,
		HTTPHeader: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
		},
	}); err != nil {
		return err
	} else {
		if d.conn != nil {
			_ = d.conn.Close(websocket.StatusServiceRestart, "")
		}

		// we assign to d.conn after because if Dial fails we'll have a nil d.conn which we don't want
		d.conn = conn
		d.log.Debug(fmt.Sprintf("connected to %s", addr))
	}

	// remove the read limit
	d.conn.SetReadLimit(math.MaxUint32)

	return nil
}

func (d *Dealer) Close() {
	d.connMu.Lock()
	d.closed = true
	d.stop = true
	conn := d.conn
	d.connMu.Unlock()

	d.signalStop()

	if conn != nil {
		_ = conn.Close(websocket.StatusGoingAway, "")
	}
}

func (d *Dealer) startReceiving() {
	d.recvLoopOnce.Do(func() {
		d.clearStopSignals()
		d.log.Tracef("starting dealer recv loop")
		d.resetPongDeadline()
		go d.pingTicker()
		go d.recvLoop()
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
			timePassed := d.timeSinceLastPong()
			if timePassed > pingInterval+timeout {
				d.log.Errorf("did not receive last pong from dealer, %.0fs passed", timePassed.Seconds())

				// closing the connection should make the read on the "recvLoop" fail,
				// continue hoping for a new connection
				d.closeConn(websocket.StatusServiceRestart)
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			conn, err := d.writeConn(ctx, websocket.MessageText, []byte("{\"type\":\"ping\"}"))
			cancel()
			d.log.Tracef("sent dealer ping")

			if err != nil {
				if d.isStopped() {
					// break early without logging if we should stop
					break loop
				}

				d.log.WithError(err).Warnf("failed sending dealer ping")

				// closing the connection should make the read on the "recvLoop" fail,
				// continue hoping for a new connection
				d.closeConnRef(conn, websocket.StatusServiceRestart)
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
			msgType, messageBytes, err := d.readConn(context.Background())

			// don't log closed error if we're stopping
			if d.isStopped() && websocket.CloseStatus(err) == websocket.StatusGoingAway {
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
	d.closeConn(websocket.StatusInternalError)

	// if we shouldn't stop, try to reconnect
	if !d.isStopped() {
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
	_, err = d.writeConn(ctx, websocket.MessageText, replyBytes)
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

	d.resetPongDeadline()
	// restart the recv loop
	go d.recvLoop()

	d.log.Debugf("re-established dealer connection")
	return nil
}

func (d *Dealer) resetPongDeadline() {
	d.lastPongLock.Lock()
	d.lastPong = time.Now().Add(pingInterval)
	d.lastPongLock.Unlock()
}

func (d *Dealer) timeSinceLastPong() time.Duration {
	d.lastPongLock.Lock()
	defer d.lastPongLock.Unlock()
	return time.Since(d.lastPong)
}

func (d *Dealer) closeConn(status websocket.StatusCode) {
	d.connMu.RLock()
	conn := d.conn
	d.connMu.RUnlock()

	d.closeConnRef(conn, status)
}

func (d *Dealer) closeConnRef(conn *websocket.Conn, status websocket.StatusCode) {
	if conn != nil {
		_ = conn.Close(status, "")
	}
}

func (d *Dealer) writeConn(ctx context.Context, typ websocket.MessageType, payload []byte) (*websocket.Conn, error) {
	d.connMu.RLock()

	if d.closed {
		d.connMu.RUnlock()
		return nil, ErrDealerClosed
	}

	conn := d.conn

	if conn == nil {
		d.connMu.RUnlock()
		return nil, fmt.Errorf("dealer connection not established")
	}

	err := conn.Write(ctx, typ, payload)
	d.connMu.RUnlock()
	return conn, err
}

func (d *Dealer) readConn(ctx context.Context) (websocket.MessageType, []byte, error) {
	d.connMu.RLock()
	conn := d.conn
	d.connMu.RUnlock()

	if conn == nil {
		return 0, nil, fmt.Errorf("dealer connection not established")
	}

	return conn.Read(ctx)
}

func (d *Dealer) signalStop() {
	select {
	case d.recvLoopStop <- struct{}{}:
	default:
	}

	select {
	case d.pingTickerStop <- struct{}{}:
	default:
	}
}

func (d *Dealer) clearStopSignals() {
	select {
	case <-d.recvLoopStop:
	default:
	}

	select {
	case <-d.pingTickerStop:
	default:
	}
}

func (d *Dealer) isStopped() bool {
	d.connMu.RLock()
	defer d.connMu.RUnlock()
	return d.stop
}
