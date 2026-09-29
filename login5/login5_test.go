//go:build test_unit

package login5

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
	pb "github.com/devgianlu/go-librespot/proto/spotify/login5/v3"
	credentialspb "github.com/devgianlu/go-librespot/proto/spotify/login5/v3/credentials"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// answer is one response of the stub login5 endpoint.
type answer struct {
	status int
	body   string
}

func okAnswer(t *testing.T) answer {
	t.Helper()
	body, err := proto.Marshal(&pb.LoginResponse{Response: &pb.LoginResponse_Ok{Ok: &pb.LoginOk{
		Username:             "user",
		AccessToken:          "token",
		StoredCredential:     []byte("credential"),
		AccessTokenExpiresIn: 3600,
	}}})
	require.NoError(t, err)
	return answer{status: http.StatusOK, body: string(body)}
}

// newTestLogin5 serves answers in order, repeating the last one, and counts
// the requests.
func newTestLogin5(t *testing.T, answers ...answer) (*Login5, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(calls.Add(1)) - 1
		a := answers[min(i, len(answers)-1)]
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)

	c := NewLogin5(&librespot.NullLogger{}, srv.Client(), "device", "client-token")
	c.baseUrl, _ = url.Parse(srv.URL + "/") // like the real base URL, with its root path
	c.newBackOff = func() backoff.BackOff { return &backoff.ZeroBackOff{} }
	return c, &calls
}

func login(c *Login5, ctx context.Context) error {
	return c.Login(ctx, &credentialspb.StoredCredential{Username: "user", Data: []byte("credential")})
}

func TestLoginRetriesAnOutage(t *testing.T) {
	c, calls := newTestLogin5(t,
		answer{http.StatusServiceUnavailable, "no healthy upstream"},
		answer{http.StatusTooManyRequests, ""},
		okAnswer(t),
	)

	require.NoError(t, login(c, context.Background()))
	require.Equal(t, int32(3), calls.Load(), "503 and 429 are retried")
	require.Equal(t, "user", c.Username())
}

func TestLoginReportsTheStatusNotAParseError(t *testing.T) {
	c, calls := newTestLogin5(t, answer{http.StatusBadRequest, "  bad request\n"})

	err := login(c, context.Background())
	var httpErr *HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.StatusCode)
	require.Equal(t, "login5 returned HTTP 400: bad request", httpErr.Error())
	require.NotContains(t, err.Error(), "wire-format")
	require.Equal(t, int32(1), calls.Load(), "other 4xx are not retried")
}

func TestLoginKeepsTheStatusWhenTheContextEnds(t *testing.T) {
	c, _ := newTestLogin5(t, answer{http.StatusServiceUnavailable, "no healthy upstream"})
	c.newBackOff = func() backoff.BackOff { return backoff.NewConstantBackOff(10 * time.Millisecond) }

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := login(c, ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	var httpErr *HTTPError
	require.ErrorAs(t, err, &httpErr, "the last answer must not be lost: %v", err)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.StatusCode)
}

func TestHTTPErrorTruncatesLongBodies(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	c, _ := newTestLogin5(t, answer{http.StatusForbidden, string(long)})

	var httpErr *HTTPError
	require.True(t, errors.As(login(c, context.Background()), &httpErr))
	require.Equal(t, maxErrorBodyLen+len("…"), len(httpErr.Body))
}

func TestHTTPErrorRetryable(t *testing.T) {
	for status, want := range map[int]bool{500: true, 502: true, 503: true, 429: true, 400: false, 401: false, 403: false, 404: false} {
		require.Equal(t, want, (&HTTPError{StatusCode: status}).Retryable(), "%d", status)
	}
}
