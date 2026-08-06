//go:build test_unit

package spclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/proto"
)

// recordedRequest is what the stub endpoint saw, so tests can assert on the
// headers, query and body the client actually put on the wire.
type recordedRequest struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
}

type RequestSuite struct {
	suite.Suite

	server   *httptest.Server
	spclient *spclient.Spclient

	mu       sync.Mutex
	received []recordedRequest

	// handler answers each request; index is the 0-based attempt number so a
	// test can fail the first call and succeed on the retry.
	handler func(attempt int, w http.ResponseWriter)

	// tokens records the forceNewToken flag of every access token lookup, which
	// is how the 401 refresh path is observed.
	tokenForced []bool
	// tokenErr makes the token lookup fail.
	tokenErr error
}

func (suite *RequestSuite) SetupTest() {
	suite.received = nil
	suite.tokenForced = nil
	suite.tokenErr = nil
	suite.handler = func(int, http.ResponseWriter) {}

	// TLS, because spclient.NewSpclient always builds an https base url.
	suite.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		suite.mu.Lock()
		attempt := len(suite.received)
		suite.received = append(suite.received, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.Query(),
			header: r.Header.Clone(),
			body:   body,
		})
		handler := suite.handler
		suite.mu.Unlock()

		handler(attempt, w)
	}))

	client := suite.server.Client()
	client.Timeout = 5 * time.Second

	host := strings.TrimPrefix(suite.server.URL, "https://")

	var err error
	suite.spclient, err = spclient.NewSpclient(
		suite.T().Context(),
		&librespot.NullLogger{},
		client,
		func(context.Context) string { return host },
		func(_ context.Context, force bool) (string, error) {
			suite.mu.Lock()
			suite.tokenForced = append(suite.tokenForced, force)
			err := suite.tokenErr
			suite.mu.Unlock()

			if err != nil {
				return "", err
			}
			if force {
				return "fresh-token", nil
			}
			return "cached-token", nil
		},
		"device-id",
		"client-token",
	)
	suite.Require().NoError(err)
}

func (suite *RequestSuite) TearDownTest() {
	suite.server.Close()
}

func (suite *RequestSuite) requests() []recordedRequest {
	suite.mu.Lock()
	defer suite.mu.Unlock()
	return append([]recordedRequest(nil), suite.received...)
}

func (suite *RequestSuite) TestSendsAuthAndClientTokenHeaders() {
	resp, err := suite.spclient.Request(suite.T().Context(), "GET", "/some/path", nil, nil, nil)
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	got := suite.requests()
	suite.Require().Len(got, 1)
	suite.Equal("GET", got[0].method)
	suite.Equal("/some/path", got[0].path)
	suite.Equal("Bearer cached-token", got[0].header.Get("Authorization"))
	suite.Equal("client-token", got[0].header.Get("Client-Token"))
}

// A body implies protobuf on this API, and the content type has to say so.
func (suite *RequestSuite) TestSetsProtobufContentTypeForBodies() {
	resp, err := suite.spclient.Request(suite.T().Context(), "POST", "/x", nil, nil, []byte("payload"))
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	got := suite.requests()
	suite.Require().Len(got, 1)
	suite.Equal("application/x-protobuf", got[0].header.Get("Content-Type"))
	suite.Equal([]byte("payload"), got[0].body)
}

func (suite *RequestSuite) TestNoContentTypeWithoutBody() {
	resp, err := suite.spclient.Request(suite.T().Context(), "GET", "/x", nil, nil, nil)
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	suite.Empty(suite.requests()[0].header.Get("Content-Type"))
}

func (suite *RequestSuite) TestPassesQueryAndCallerHeaders() {
	header := http.Header{"X-Spotify-Connection-Id": []string{"conn-id"}}
	query := url.Values{"notify": []string{"true"}, "other": []string{"1"}}

	resp, err := suite.spclient.Request(suite.T().Context(), "PUT", "/x", query, header, nil)
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	got := suite.requests()[0]
	suite.Equal("conn-id", got.header.Get("X-Spotify-Connection-Id"))
	suite.Equal("true", got.query.Get("notify"))
	suite.Equal("1", got.query.Get("other"))
}

// A 401 means the token went stale: the client must retry once with a freshly
// minted token rather than surfacing the failure.
func (suite *RequestSuite) TestRetriesWithFreshTokenAfterUnauthorized() {
	suite.handler = func(attempt int, w http.ResponseWriter) {
		if attempt == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}

	resp, err := suite.spclient.Request(suite.T().Context(), "GET", "/x", nil, nil, nil)
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()
	suite.Equal(http.StatusOK, resp.StatusCode)

	got := suite.requests()
	suite.Require().Len(got, 2)
	suite.Equal("Bearer cached-token", got[0].header.Get("Authorization"))
	suite.Equal("Bearer fresh-token", got[1].header.Get("Authorization"),
		"the retry must carry a newly minted token")

	suite.mu.Lock()
	defer suite.mu.Unlock()
	suite.Equal([]bool{false, true}, suite.tokenForced,
		"the second lookup must force a refresh")
}

func (suite *RequestSuite) TestRetriesOnTransientStatuses() {
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		suite.Run(http.StatusText(status), func() {
			suite.SetupTest()
			defer suite.TearDownTest()

			suite.handler = func(attempt int, w http.ResponseWriter) {
				if attempt == 0 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusOK)
			}

			resp, err := suite.spclient.Request(suite.T().Context(), "GET", "/x", nil, nil, nil)
			suite.Require().NoError(err)
			defer func() { _ = resp.Body.Close() }()

			suite.Equal(http.StatusOK, resp.StatusCode)
			suite.Len(suite.requests(), 2)
		})
	}
}

// A retried request must carry its body again: the first attempt consumed it,
// and a silently empty retry would be worse than a failure.
func (suite *RequestSuite) TestRecreatesBodyOnRetry() {
	suite.handler = func(attempt int, w http.ResponseWriter) {
		if attempt == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}

	resp, err := suite.spclient.Request(suite.T().Context(), "POST", "/x", nil, nil, []byte("payload"))
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	got := suite.requests()
	suite.Require().Len(got, 2)
	suite.Equal([]byte("payload"), got[0].body)
	suite.Equal([]byte("payload"), got[1].body, "the retry must resend the body")
}

// Anything that is not 401 or transient is the caller's to interpret, so it
// comes back as-is instead of being retried.
func (suite *RequestSuite) TestDoesNotRetryOtherStatuses() {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
	} {
		suite.Run(http.StatusText(status), func() {
			suite.SetupTest()
			defer suite.TearDownTest()

			suite.handler = func(_ int, w http.ResponseWriter) { w.WriteHeader(status) }

			resp, err := suite.spclient.Request(suite.T().Context(), "GET", "/x", nil, nil, nil)
			suite.Require().NoError(err)
			defer func() { _ = resp.Body.Close() }()

			suite.Equal(status, resp.StatusCode)
			suite.Len(suite.requests(), 1)
		})
	}
}

// A token that cannot be minted is permanent: retrying cannot fix it, and the
// request must fail without ever reaching the network.
func (suite *RequestSuite) TestFailsPermanentlyWhenTokenUnavailable() {
	suite.tokenErr = context.DeadlineExceeded

	_, err := suite.spclient.Request(suite.T().Context(), "GET", "/x", nil, nil, nil)
	suite.Require().Error(err)
	suite.Contains(err.Error(), "failed obtaining spclient access token")
	suite.Empty(suite.requests(), "no request should have been sent")
}

func (suite *RequestSuite) TestHonoursContextCancellation() {
	ctx, cancel := context.WithCancel(suite.T().Context())
	cancel()

	_, err := suite.spclient.Request(ctx, "GET", "/x", nil, nil, nil)
	suite.Require().Error(err)
	suite.ErrorIs(err, context.Canceled)
}

// RequestNoRedirect exists for endpoints that answer with a Location instead of
// a body, so the redirect itself has to come back unfollowed.
func (suite *RequestSuite) TestRequestNoRedirectReturnsTheRedirect() {
	suite.handler = func(_ int, w http.ResponseWriter) {
		w.Header().Set("Location", "https://example.com/elsewhere")
		w.WriteHeader(http.StatusFound)
	}

	resp, err := suite.spclient.RequestNoRedirect(suite.T().Context(), "GET", "/x", nil, nil, nil)
	suite.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	suite.Equal(http.StatusFound, resp.StatusCode)
	suite.Equal("https://example.com/elsewhere", resp.Header.Get("Location"))
	suite.Len(suite.requests(), 1, "the redirect must not have been followed")
}

// The endpoint answers 204, and only 204 counts as success.
func (suite *RequestSuite) TestPutConnectStateInactiveTargetsTheDevice() {
	suite.handler = func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

	suite.Require().NoError(
		suite.spclient.PutConnectStateInactive(suite.T().Context(), "conn-id", true))

	got := suite.requests()[0]
	suite.Equal("PUT", got.method)
	suite.Equal("/connect-state/v1/devices/device-id/inactive", got.path)
	suite.Equal("true", got.query.Get("notify"))
	suite.Equal("conn-id", got.header.Get("X-Spotify-Connection-Id"))
}

func (suite *RequestSuite) TestPutConnectStateInactiveRejectsOtherStatuses() {
	suite.handler = func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

	err := suite.spclient.PutConnectStateInactive(suite.T().Context(), "conn-id", false)
	suite.Require().Error(err)
	suite.Contains(err.Error(), "failed with status 200")
	suite.Equal("false", suite.requests()[0].query.Get("notify"))
}

// The backend answers a state update with the cluster, which is where a device
// learns the public address it cannot see for itself.
func (suite *RequestSuite) TestPutConnectStateReturnsCluster() {
	body, err := proto.Marshal(&connectpb.Cluster{
		Device: map[string]*connectpb.DeviceInfo{
			"device-id": {Name: "test", PublicIp: "203.0.113.7"},
		},
	})
	suite.Require().NoError(err)

	suite.handler = func(_ int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}

	cluster, err := suite.spclient.PutConnectState(suite.T().Context(), "conn-id", &connectpb.PutStateRequest{})
	suite.Require().NoError(err)
	suite.Equal("203.0.113.7", cluster.Device["device-id"].PublicIp)

	got := suite.requests()[0]
	suite.Equal("PUT", got.method)
	suite.Equal("/connect-state/v1/devices/device-id", got.path)
	suite.Equal("conn-id", got.header.Get("X-Spotify-Connection-Id"))
}

func (suite *RequestSuite) TestPutConnectStateRejectsUnparseableCluster() {
	suite.handler = func(_ int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not a protobuf at all"))
	}

	_, err := suite.spclient.PutConnectState(suite.T().Context(), "conn-id", &connectpb.PutStateRequest{})
	suite.Require().Error(err)
	suite.Contains(err.Error(), "failed unmarshalling Cluster")
}

func TestRequestSuite(t *testing.T) {
	suite.Run(t, new(RequestSuite))
}
