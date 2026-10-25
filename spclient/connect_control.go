package spclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// RemoteError is a refusal by the connect-state service of a request aimed at
// another device.
type RemoteError struct {
	Status int
	Body   string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("connect-state request failed with status %d: %s", e.Status, e.Body)
}

// ConnectControl sends a request that controls another device of the account:
// the service forwards it from this device (from) to the target (to). kind is
// the path segment after /connect-state/v1/, e.g. "connect/transfer",
// "player/command" or "connect/volume". The requests act on the remote
// device, so none is repeated on failure.
func (c *Spclient) ConnectControl(ctx context.Context, method, kind, spotConnId, to string, body any) ([]byte, error) {
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling %s request: %w", kind, err)
	}

	path := fmt.Sprintf("/connect-state/v1/%s/from/%s/to/%s", kind, url.PathEscape(c.deviceId), url.PathEscape(to))
	resp, err := c.RequestOnce(ctx, method, path, nil, http.Header{
		"X-Spotify-Connection-Id": []string{spotConnId},
		"Content-Type":            []string{"application/json"},
	}, reqBody)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		c.log.Debugf("connect-state %s failed with status %d: %s", kind, resp.StatusCode, respBody)
		return nil, &RemoteError{Status: resp.StatusCode, Body: string(respBody)}
	}

	c.log.Debugf("connect-state %s answered %d: %s", kind, resp.StatusCode, respBody)
	return respBody, nil
}
