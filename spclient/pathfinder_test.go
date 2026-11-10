//go:build test_unit

package spclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/require"
)

// pathfinderStub is a pathfinder stand-in: handler answers each attempt, and
// what the client sent is recorded.
type pathfinderStub struct {
	spc     *Spclient
	handler func(attempt int, w http.ResponseWriter)

	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
}

func newPathfinderStub(t *testing.T, handler func(attempt int, w http.ResponseWriter)) *pathfinderStub {
	t.Helper()

	stub := &pathfinderStub{handler: handler}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		stub.mu.Lock()
		attempt := len(stub.requests)
		stub.requests = append(stub.requests, r.Clone(context.Background()))
		stub.bodies = append(stub.bodies, body)
		stub.mu.Unlock()

		stub.handler(attempt, w)
	}))
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL + "/pathfinder/v2/query")
	require.NoError(t, err)

	client := server.Client()
	client.Timeout = 5 * time.Second

	stub.spc = &Spclient{
		log:           &librespot.NullLogger{},
		client:        client,
		clientToken:   "client-token",
		pathfinderUrl: u,
		accessToken: func(context.Context, bool) (string, error) {
			return "token", nil
		},
	}

	return stub
}

func (s *pathfinderStub) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *pathfinderStub) query(ctx context.Context) ([]byte, error) {
	return s.spc.PathfinderQuery(ctx, "searchTracks", "abc123", map[string]any{"searchTerm": "daft punk", "limit": 20})
}

func answer(status int, body string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const pathfinderOkBody = `{"data":{"searchV2":{"query":"daft punk"}},"extensions":{}}`

// pathfinderFailedBody is an operation that failed while running: a 200 whose
// data is all null. Whether the one seen live carried GraphQL errors was not
// recorded; this body has one, a body without any is run again as well.
const pathfinderFailedBody = `{"data":{"searchV2":null},"errors":[{"message":"Internal error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`

func TestPathfinderQuerySendsAPersistedQuery(t *testing.T) {
	stub := newPathfinderStub(t, answer(http.StatusOK, pathfinderOkBody))

	data, err := stub.query(t.Context())
	require.NoError(t, err)
	require.JSONEq(t, `{"searchV2":{"query":"daft punk"}}`, string(data))

	require.Equal(t, 1, stub.attempts())
	req := stub.requests[0]
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "/pathfinder/v2/query", req.URL.Path)
	require.Empty(t, req.URL.RawQuery)
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Equal(t, "application/json", req.Header.Get("Accept"))
	require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
	require.Equal(t, "client-token", req.Header.Get("Client-Token"))
	require.Equal(t, librespot.SpotifyLikeUserAgent(), req.Header.Get("User-Agent"))
	require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "Spotify/"),
		"pathfinder refuses Go's default and go-librespot's own user agent with a 403")
	require.JSONEq(t, `{
		"variables": {"searchTerm": "daft punk", "limit": 20},
		"operationName": "searchTracks",
		"extensions": {"persistedQuery": {"version": 1, "sha256Hash": "abc123"}}
	}`, string(stub.bodies[0]))
}

func TestPathfinderQueryRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		body     string
		outdated bool
		messages []string
	}{
		"outdated hash": {
			status:   http.StatusPreconditionFailed,
			body:     `"Invalid query hash"`,
			outdated: true,
			messages: []string{"Invalid query hash"},
		},
		"graphql errors": {
			status:   http.StatusBadRequest,
			body:     `{"errors":[{"message":"Unknown operation","extensions":{"code":"GRAPHQL_UNKNOWN_OPERATION_NAME"}}]}`,
			messages: []string{"Unknown operation [GRAPHQL_UNKNOWN_OPERATION_NAME]"},
		},
		"client not allowed": {
			status:   http.StatusForbidden,
			body:     "{\n  \"error\": {\n    \"status\": 403,\n    \"message\": \"Client/request not allowed\"\n  }\n}",
			messages: []string{"Client/request not allowed"},
		},
		"bad request as text": {
			status:   http.StatusBadRequest,
			body:     "bad things",
			messages: []string{"bad things"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newPathfinderStub(t, answer(tc.status, tc.body))

			_, err := stub.query(t.Context())
			var pe *PathfinderError
			require.ErrorAs(t, err, &pe)
			require.Equal(t, "searchTracks", pe.Operation)
			require.Equal(t, tc.status, pe.StatusCode)
			require.Equal(t, tc.messages, pe.Messages)
			require.Equal(t, tc.outdated, pe.IsQueryHashOutdated())
			require.Equal(t, 1, stub.attempts(), "a refusal is not sent again")
		})
	}
}

func TestPathfinderQueryRateLimited(t *testing.T) {
	stub := newPathfinderStub(t, func(_ int, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := stub.query(t.Context())
	var rl *RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 7*time.Second, rl.RetryAfter)
	var pe *PathfinderError
	require.ErrorAs(t, err, &pe, "the rate limit still names the operation")
	require.Equal(t, http.StatusTooManyRequests, pe.StatusCode)
	require.Equal(t, 1, stub.attempts(), "a rate limit is left to the caller")
}

func TestPathfinderQueryFailedOperation(t *testing.T) {
	t.Run("run again", func(t *testing.T) {
		stub := newPathfinderStub(t, func(attempt int, w http.ResponseWriter) {
			if attempt == 0 {
				_, _ = w.Write([]byte(pathfinderFailedBody))
				return
			}
			_, _ = w.Write([]byte(pathfinderOkBody))
		})

		data, err := stub.query(t.Context())
		require.NoError(t, err)
		require.JSONEq(t, `{"searchV2":{"query":"daft punk"}}`, string(data))
		require.Equal(t, 2, stub.attempts())
	})

	t.Run("without GraphQL errors", func(t *testing.T) {
		stub := newPathfinderStub(t, func(attempt int, w http.ResponseWriter) {
			if attempt == 0 {
				_, _ = w.Write([]byte(`{"data":{"searchV2":null}}`))
				return
			}
			_, _ = w.Write([]byte(pathfinderOkBody))
		})

		_, err := stub.query(t.Context())
		require.NoError(t, err)
		require.Equal(t, 2, stub.attempts())
	})

	t.Run("until it gives up", func(t *testing.T) {
		stub := newPathfinderStub(t, answer(http.StatusOK, pathfinderFailedBody))

		_, err := stub.query(t.Context())
		var pe *PathfinderError
		require.ErrorAs(t, err, &pe)
		require.Equal(t, http.StatusOK, pe.StatusCode)
		require.Equal(t, []string{"Internal error [INTERNAL_SERVER_ERROR]"}, pe.Messages)
		require.Equal(t, 3, stub.attempts())
	})
}

func TestPathfinderQueryGraphQLErrors(t *testing.T) {
	t.Run("next to data, the data is returned", func(t *testing.T) {
		stub := newPathfinderStub(t, answer(http.StatusOK,
			`{"data":{"searchV2":{"query":"x"}},"errors":[{"message":"one field failed"}]}`))

		data, err := stub.query(t.Context())
		require.NoError(t, err)
		require.JSONEq(t, `{"searchV2":{"query":"x"}}`, string(data))
	})

	t.Run("without data, an error", func(t *testing.T) {
		stub := newPathfinderStub(t, answer(http.StatusOK, `{"errors":[{"message":"Validation error"}]}`))

		_, err := stub.query(t.Context())
		var pe *PathfinderError
		require.ErrorAs(t, err, &pe)
		require.Equal(t, []string{"Validation error"}, pe.Messages)
		require.Equal(t, 1, stub.attempts())
	})

	t.Run("no data and no errors", func(t *testing.T) {
		stub := newPathfinderStub(t, answer(http.StatusOK, `{"data":null}`))

		_, err := stub.query(t.Context())
		var pe *PathfinderError
		require.ErrorAs(t, err, &pe)
		require.Equal(t, 1, stub.attempts())
	})

	t.Run("not json", func(t *testing.T) {
		stub := newPathfinderStub(t, answer(http.StatusOK, `<html>`))

		_, err := stub.query(t.Context())
		require.Error(t, err)
		require.Equal(t, 1, stub.attempts())
	})
}

func TestPathfinderQueryOversizedResponse(t *testing.T) {
	stub := newPathfinderStub(t, func(_ int, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"data":"`))
		_, _ = w.Write([]byte(strings.Repeat("a", maxPathfinderBody)))
		_, _ = w.Write([]byte(`"}`))
	})

	_, err := stub.query(t.Context())
	require.ErrorContains(t, err, "exceeds")
	require.Equal(t, 1, stub.attempts())
}

func TestPathfinderQueryCancelled(t *testing.T) {
	stub := newPathfinderStub(t, answer(http.StatusOK, pathfinderFailedBody))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := stub.query(ctx)
	require.True(t, errors.Is(err, context.Canceled), "got %v", err)
}

func TestPathfinderErrorMessage(t *testing.T) {
	require.Equal(t, "pathfinder searchTracks failed with status 403: Client/request not allowed",
		(&PathfinderError{Operation: "searchTracks", StatusCode: 403, Messages: []string{"Client/request not allowed"}}).Error())
	require.Equal(t, "pathfinder searchAlbums failed with status 412, persisted query hash outdated",
		(&PathfinderError{Operation: "searchAlbums", StatusCode: 412}).Error())
}
