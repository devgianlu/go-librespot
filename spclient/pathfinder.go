package spclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
)

// pathfinderQueryUrl is the endpoint of pathfinder, the GraphQL service the
// official clients search through. It only runs persisted queries.
const pathfinderQueryUrl = "https://api-partner.spotify.com/pathfinder/v2/query"

// maxPathfinderBody caps a pathfinder response. A search page is a few hundred
// KiB; a larger answer is refused rather than truncated.
const maxPathfinderBody = 8 << 20

// pathfinderRetryInterval is the wait before running a query again whose
// operation failed with no result at all.
const pathfinderRetryInterval = 250 * time.Millisecond

// PathfinderError is a pathfinder query that failed: a status other than 200,
// or a 200 carrying GraphQL errors and no result.
type PathfinderError struct {
	Operation  string
	StatusCode int
	// Messages are the GraphQL error messages, or the body when it holds none.
	Messages []string
}

func (e *PathfinderError) Error() string {
	msg := fmt.Sprintf("pathfinder %s failed with status %d", e.Operation, e.StatusCode)
	if e.IsQueryHashOutdated() {
		msg += ", persisted query hash outdated"
	}
	if len(e.Messages) > 0 {
		msg += ": " + strings.Join(e.Messages, "; ")
	}
	return msg
}

// IsQueryHashOutdated reports a 412: pathfinder no longer knows the hash of
// the persisted query, which changes whenever Spotify rebuilds that query.
func (e *PathfinderError) IsQueryHashOutdated() bool {
	return e.StatusCode == http.StatusPreconditionFailed
}

type pathfinderRequest struct {
	Variables     any                  `json:"variables"`
	OperationName string               `json:"operationName"`
	Extensions    pathfinderExtensions `json:"extensions"`
}

type pathfinderExtensions struct {
	PersistedQuery pathfinderPersistedQuery `json:"persistedQuery"`
}

type pathfinderPersistedQuery struct {
	Version    int    `json:"version"`
	Sha256Hash string `json:"sha256Hash"`
}

type pathfinderGraphQLError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type pathfinderResponse struct {
	Data   json.RawMessage          `json:"data"`
	Errors []pathfinderGraphQLError `json:"errors"`
	// Error is Spotify's own error body, as in a 403.
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// PathfinderQuery runs a read-only persisted query, an operation name and the
// sha256 hash of its query document, and returns the data of its result.
//
// The request goes through the same transport as every spclient request, with
// the User-Agent pathfinder accepts (it refuses UserAgent() with a 403). A 429
// is a *RateLimitedError, any other failed status a *PathfinderError. A result
// with GraphQL errors next to its data is returned, the errors logged; one
// whose fields are all null failed as a whole and is run again, at most twice.
func (c *Spclient) PathfinderQuery(ctx context.Context, operationName, sha256Hash string, variables any) (json.RawMessage, error) {
	body, err := json.Marshal(pathfinderRequest{
		Variables:     variables,
		OperationName: operationName,
		Extensions: pathfinderExtensions{
			PersistedQuery: pathfinderPersistedQuery{Version: 1, Sha256Hash: sha256Hash},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed marshalling pathfinder %s request: %w", operationName, err)
	}

	return backoff.RetryWithData(func() (json.RawMessage, error) {
		resp, err := c.innerRequest(ctx, http.MethodPost, c.pathfinderUrl.JoinPath(), nil, http.Header{
			"Content-Type": []string{"application/json"},
			"Accept":       []string{"application/json"},
			"User-Agent":   []string{librespot.SpotifyLikeUserAgent()},
		}, body)
		if err != nil {
			return nil, backoff.Permanent(err)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxPathfinderBody+1))
		if err != nil {
			return nil, backoff.Permanent(fmt.Errorf("failed reading pathfinder %s response: %w", operationName, err))
		} else if len(respBody) > maxPathfinderBody {
			return nil, backoff.Permanent(fmt.Errorf("pathfinder %s response exceeds %d bytes", operationName, maxPathfinderBody))
		}

		if resp.StatusCode != http.StatusOK {
			err := &PathfinderError{Operation: operationName, StatusCode: resp.StatusCode, Messages: pathfinderMessages(respBody)}
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, backoff.Permanent(&RateLimitedError{RetryAfter: parseRetryAfter(resp.Header), err: err})
			}
			return nil, backoff.Permanent(err)
		}

		return c.pathfinderData(operationName, respBody)
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewConstantBackOff(pathfinderRetryInterval), 2), ctx))
}

// pathfinderData reads the data out of a 200 response. Only an operation that
// ran and failed as a whole, its data all null, is worth running again.
func (c *Spclient) pathfinderData(operationName string, respBody []byte) (json.RawMessage, error) {
	var resp pathfinderResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, backoff.Permanent(fmt.Errorf("failed unmarshalling pathfinder %s response: %w", operationName, err))
	}

	messages := graphQLMessages(resp.Errors)
	if len(resp.Data) == 0 || bytes.Equal(resp.Data, []byte("null")) {
		if len(messages) == 0 {
			messages = []string{"no data"}
		}
		return nil, backoff.Permanent(&PathfinderError{Operation: operationName, StatusCode: http.StatusOK, Messages: messages})
	} else if allFieldsNull(resp.Data) {
		if len(messages) == 0 {
			messages = []string{"no result"}
		}
		return nil, &PathfinderError{Operation: operationName, StatusCode: http.StatusOK, Messages: messages}
	} else if len(messages) > 0 {
		c.log.Debugf("pathfinder %s returned partial data: %s", operationName, strings.Join(messages, "; "))
	}

	return resp.Data, nil
}

// allFieldsNull reports whether data is an object holding nothing but nulls,
// such as {"searchV2": null}.
func allFieldsNull(data json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false
	}
	for _, value := range fields {
		if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func graphQLMessages(errs []pathfinderGraphQLError) []string {
	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		msg := e.Message
		if e.Extensions.Code != "" {
			msg += " [" + e.Extensions.Code + "]"
		}
		messages = append(messages, msg)
	}
	return messages
}

// pathfinderMessages reads the reason out of an error body: GraphQL errors,
// Spotify's {"error": {"message": ...}}, a bare JSON string such as "Invalid
// query hash", or else the text.
func pathfinderMessages(body []byte) []string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}

	var resp pathfinderResponse
	if err := json.Unmarshal(body, &resp); err == nil && len(resp.Errors) > 0 {
		return graphQLMessages(resp.Errors)
	} else if err == nil && resp.Error.Message != "" {
		return []string{resp.Error.Message}
	}

	var msg string
	if err := json.Unmarshal(body, &msg); err == nil {
		return []string{msg}
	}

	const maxText = 200
	text := string(body)
	if len(text) > maxText {
		text = text[:maxText] + "…"
	}
	return []string{text}
}
