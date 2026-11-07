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
	// A ping goes out every pingInterval and the connection is dropped when
	// its pong has not come back within pongTimeout, so a connection that went
	// silent is replaced within pingInterval+pongTimeout.
	pingInterval = 15 * time.Second
	pongTimeout  = 10 * time.Second
	timeout      = 10 * time.Second
)

var ErrDealerClosed = errors.New("dealer closed")

type Dealer struct {
	log librespot.Logger

	client *http.Client

	addr        librespot.GetAddressFunc
	accessToken librespot.GetLogin5TokenFunc

	conn *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc

	done         chan struct{}
	closeOnce    sync.Once
	recvLoopOnce sync.Once
	lastPong     time.Time
	lastPongLock sync.Mutex

	// readerBusy is set while recvLoop hands a message or a request over to
	// its receiver, during which it reads nothing, and readerFreeSince is when
	// it last went back to reading. Both are guarded by lastPongLock.
	readerBusy      bool
	readerFreeSince time.Time

	// connMu protects conn pointer state.
	connMu sync.RWMutex

	messageReceivers     []messageReceiver
	messageReceiversLock sync.RWMutex

	requestReceivers     map[string]requestReceiver
	requestReceiversLock sync.RWMutex
}

func NewDealer(log librespot.Logger, client *http.Client, dealerAddr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc) *Dealer {
	ctx, cancel := context.WithCancel(context.Background())
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
		ctx:              ctx,
		cancel:           cancel,
		done:             make(chan struct{}),
		requestReceivers: map[string]requestReceiver{},
	}
}

func (d *Dealer) Connect(ctx context.Context) error {
	d.connMu.Lock()
	defer d.connMu.Unlock()

	select {
	case <-d.done:
		return ErrDealerClosed
	default:
	}

	if d.conn != nil {
		d.log.Debugf("dealer connection already opened")
		return nil
	}

	return d.connect(ctx)
}

func (d *Dealer) connect(ctx context.Context) error {
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
	d.closeOnce.Do(func() {
		close(d.done)

		// Before cancelling, so the peer still gets a clean going-away frame:
		// cancelling the context tears the websocket down abruptly.
		d.closeConn(websocket.StatusGoingAway)

		d.cancel()
	})
}

func (d *Dealer) startReceiving() {
	d.recvLoopOnce.Do(func() {
		d.log.Tracef("starting dealer recv loop")
		d.resetPongDeadline()
		go d.pingTicker()
		go d.recvLoop()
	})
}

func (d *Dealer) pingTicker() {
	ticker := time.NewTicker(pingInterval)

	// pongDeadline fires pongTimeout after the last ping went out on pingConn,
	// and is nil while no ping is waiting for its pong.
	var pongDeadline <-chan time.Time
	var pingConn *websocket.Conn
	var pingSent time.Time

loop:
	for {
		select {
		case <-d.done:
			break loop
		case <-pongDeadline:
			pongDeadline = nil
			answered, wait := d.pongWait(pingSent)
			if answered {
				continue
			} else if wait > 0 {
				pongDeadline = time.After(wait)
				continue
			}

			d.log.Errorf("did not receive pong from dealer within %.0fs", pongTimeout.Seconds())

			// The link is presumed dead, so a close handshake would only wait
			// for its own timeout. Closing should make the read on the
			// "recvLoop" fail, continue hoping for a new connection.
			_ = pingConn.CloseNow()
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(d.ctx, timeout)
			sent := time.Now()
			conn, err := d.writeConn(ctx, websocket.MessageText, []byte("{\"type\":\"ping\"}"))
			cancel()

			if err != nil {
				select {
				case <-d.done:
					break loop
				default:
				}

				d.log.WithError(err).Warnf("failed sending dealer ping")

				// closing the connection should make the read on the "recvLoop" fail,
				// continue hoping for a new connection
				d.closeConnRef(conn, websocket.StatusServiceRestart)
				continue
			}

			// While a ping is still waiting for its pong, keep timing that one:
			// a later ping would push back the deadline of a dead connection.
			if answered, _ := d.pongWait(pingSent); pongDeadline == nil || answered {
				pingConn, pingSent = conn, sent
				pongDeadline = time.After(pongTimeout)
			}
		}
	}

	ticker.Stop()
}

func (d *Dealer) recvLoop() {
loop:
	for {
		select {
		case <-d.done:
			break loop
		default:
			// no need to hold the connMu since reconnection happens in this routine
			msgType, messageBytes, err := d.readConn(d.ctx)

			// don't log closed error if we're shutting down
			if err != nil {
				select {
				case <-d.done:
					if websocket.CloseStatus(err) == websocket.StatusGoingAway {
						d.log.Debugf("dealer connection closed")
					}
					break loop
				default:
				}

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
				d.setReaderBusy(true)
				d.handleMessage(&message)
				d.setReaderBusy(false)
				break
			case "request":
				d.setReaderBusy(true)
				d.handleRequest(&message)
				d.setReaderBusy(false)
				break
			case "ping":
				// we never receive ping messages
				break
			case "pong":
				d.lastPongLock.Lock()
				d.lastPong = time.Now()
				d.lastPongLock.Unlock()
				break
			default:
				d.log.Warnf("unknown dealer message type: %s", message.Type)
				break
			}
		}
	}

	// always close as we might end up here because of application errors
	d.closeConn(websocket.StatusInternalError)

	select {
	case <-d.done:
	default:
		d.connMu.Lock()
		if err := backoff.Retry(d.reconnect, backoff.WithContext(backoff.NewExponentialBackOff(), d.ctx)); err != nil {
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

	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	_, err = d.writeConn(ctx, websocket.MessageText, replyBytes)
	cancel()
	if err != nil {
		return fmt.Errorf("failed sending dealer reply: %w", err)
	}

	return nil
}

func (d *Dealer) reconnect() error {
	if err := d.connect(d.ctx); err != nil {
		return err
	}

	d.resetPongDeadline()
	// restart the recv loop
	go d.recvLoop()

	d.log.Debugf("re-established dealer connection")
	return nil
}

// resetPongDeadline counts a new connection as an answer to the pings sent on
// the previous one.
func (d *Dealer) resetPongDeadline() {
	d.lastPongLock.Lock()
	d.lastPong = time.Now()
	d.lastPongLock.Unlock()
}

func (d *Dealer) setReaderBusy(busy bool) {
	d.lastPongLock.Lock()
	d.readerBusy = busy
	if !busy {
		d.readerFreeSince = time.Now()
	}
	d.lastPongLock.Unlock()
}

// pongWait reports whether the ping sent at sent was answered and, if not,
// how much longer to wait for its pong: zero or less means it is overdue.
//
// A pong is seen only once recvLoop reads it, and recvLoop reads nothing
// while a receiver takes a message or a request, which lasts as long as the
// player takes to process a command (a transfer makes spclient requests of up
// to 30 s). So the pong's time runs only while recvLoop is reading: from the
// ping, or from when recvLoop went back to reading if that is later. Closing
// the connection sooner would gain nothing, as recvLoop cannot reconnect
// before the receiver lets it go, and it would lose the command's reply.
func (d *Dealer) pongWait(sent time.Time) (answered bool, wait time.Duration) {
	d.lastPongLock.Lock()
	defer d.lastPongLock.Unlock()

	if !d.lastPong.Before(sent) {
		return true, 0
	} else if d.readerBusy {
		return false, pongTimeout
	}

	from := sent
	if d.readerFreeSince.After(from) {
		from = d.readerFreeSince
	}
	return false, time.Until(from.Add(pongTimeout))
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
	select {
	case <-d.done:
		d.connMu.RUnlock()
		return nil, ErrDealerClosed
	default:
	}

	conn := d.conn
	d.connMu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("dealer connection not established")
	}

	err := conn.Write(ctx, typ, payload)
	if err != nil {
		select {
		case <-d.done:
			return conn, ErrDealerClosed
		default:
		}
	}

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
