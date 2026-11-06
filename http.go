package go_librespot

import (
	"net/http"
	"time"
)

// NewHttpClient returns the client used for every HTTP request to Spotify.
//
// It health-checks pooled HTTP/2 connections. Without that, a connection whose
// network path went away stays in the pool: a request that times out only
// resets its stream, so every later request to that host is sent on the same
// dead connection and times out too. With it, a connection that receives
// nothing for 15 seconds is pinged, and closed if the ping goes unanswered for
// 10 seconds, so the next request dials a new one.
func NewHttpClient() *http.Client {
	return newHttpClient(15*time.Second, 10*time.Second)
}

func newHttpClient(pingAfter, pingTimeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.HTTP2 = &http.HTTP2Config{
		SendPingTimeout: pingAfter,
		PingTimeout:     pingTimeout,
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: t}
}
