//go:build test_unit

package go_librespot

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// silenceableConn drops everything read and written once silenced, like a
// connection whose network path went away without a FIN or an RST.
type silenceableConn struct {
	net.Conn
	silent *atomic.Bool
}

func (c *silenceableConn) Read(b []byte) (int, error) {
	for {
		n, err := c.Conn.Read(b)
		if err != nil || !c.silent.Load() {
			return n, err
		}
	}
}

func (c *silenceableConn) Write(b []byte) (int, error) {
	if c.silent.Load() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

type silenceableListener struct {
	net.Listener
	mu       sync.Mutex
	accepted []*atomic.Bool
}

func (l *silenceableListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	silent := new(atomic.Bool)
	l.mu.Lock()
	l.accepted = append(l.accepted, silent)
	l.mu.Unlock()
	return &silenceableConn{Conn: conn, silent: silent}, nil
}

func (l *silenceableListener) silenceAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, silent := range l.accepted {
		silent.Store(true)
	}
}

func (l *silenceableListener) acceptCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.accepted)
}

func TestHttpClientDropsSilentHttp2Connection(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	lis := &silenceableListener{Listener: srv.Listener}
	srv.Listener = lis
	srv.StartTLS()
	defer srv.Close()

	client := newHttpClient(200*time.Millisecond, 200*time.Millisecond)
	client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()

	get := func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return resp, err
	}

	resp, err := get(context.Background())
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if resp.ProtoMajor != 2 {
		t.Fatalf("first request used %s, the test needs HTTP/2", resp.Proto)
	}

	lis.silenceAll()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err = get(ctx)
	if err == nil || !strings.Contains(err.Error(), "client connection lost") {
		t.Fatalf("request on the silent connection after %s: got %v, want the connection closed as lost", time.Since(start), err)
	}

	if _, err := get(context.Background()); err != nil {
		t.Fatalf("request after the lost connection: %v", err)
	}
	if n := lis.acceptCount(); n != 2 {
		t.Fatalf("server accepted %d connections, want 2 (the silent one, then a new one)", n)
	}
}
