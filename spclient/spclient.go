package spclient

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	storagepb "go-librespot/proto/spotify/download"
	metadatapb "go-librespot/proto/spotify/metadata"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/url"
)

type Spclient struct {
	client *http.Client

	baseUrl     *url.URL
	clientToken string
	deviceId    string

	accessToken librespot.GetLogin5TokenFunc
}

func NewSpclient(addr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc, deviceId, clientToken string) (*Spclient, error) {
	baseUrl, err := url.Parse(fmt.Sprintf("https://%s/", addr()))
	if err != nil {
		return nil, fmt.Errorf("invalid spclient base url: %w", err)
	}

	return &Spclient{
		client:      &http.Client{},
		baseUrl:     baseUrl,
		clientToken: clientToken,
		deviceId:    deviceId,
		accessToken: accessToken,
	}, nil
}

func (c *Spclient) request(method string, path string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	accessToken, err := c.accessToken()
	if err != nil {
		return nil, fmt.Errorf("failed obtaining spclient access token: %w", err)
	}

	reqUrl := c.baseUrl.JoinPath(path)
	if query != nil {
		reqUrl.RawQuery = query.Encode()
	}

	req := &http.Request{
		URL:    reqUrl,
		Method: method,
		Header: http.Header{},
	}

	if header != nil {
		for name, values := range header {
			req.Header[name] = values
		}
	}

	req.Header.Set("Client-Token", c.clientToken)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

	if body != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.Body, _ = req.GetBody()
	}

	resp, err := backoff.RetryWithData(func() (*http.Response, error) { return c.client.Do(req) }, backoff.NewExponentialBackOff())
	if err != nil {
		return nil, fmt.Errorf("spclient request failed: %w", err)
	}

	return resp, nil
}

type putStateError struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func (c *Spclient) PutConnectState(spotConnId string, reqProto *connectpb.PutStateRequest) error {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return fmt.Errorf("failed marshalling PutStateRequest: %w", err)
	}

	resp, err := c.request(
		"PUT",
		fmt.Sprintf("/connect-state/v1/devices/%s", c.deviceId),
		nil,
		http.Header{
			"X-Spotify-Connection-Id": []string{spotConnId},
			"Content-Type":            []string{"application/x-protobuf"},
		},
		reqBody,
	)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		var putError putStateError
		if err := json.NewDecoder(resp.Body).Decode(&putError); err != nil {
			return fmt.Errorf("failed reading error response: %w", err)
		}

		return fmt.Errorf("put state request failed with status %d: %s", resp.StatusCode, putError.Message)
	} else {
		log.Debugf("put connect state because %s", reqProto.PutStateReason)
		return nil
	}
}

func (c *Spclient) ResolveStorageInteractive(fileId []byte, prefetch bool) (*storagepb.StorageResolveResponse, error) {
	var path string
	if prefetch {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive_prefetch/%s", hex.EncodeToString(fileId))
	} else {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive/%s", hex.EncodeToString(fileId))
	}

	resp, err := c.request("GET", path, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from storage resolve: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp storagepb.StorageResolveResponse
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling StorageResolveResponse: %w", err)
	}

	return &protoResp, nil
}

func (c *Spclient) MetadataForTrack(track librespot.TrackId) (*metadatapb.Track, error) {
	resp, err := c.request("GET", fmt.Sprintf("/metadata/4/track/%s", track.Hex()), nil, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from track metadata: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp metadatapb.Track
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling Track: %w", err)
	}

	return &protoResp, nil
}

func (c *Spclient) ContextResolve(uri string) (*connectpb.Context, error) {
	resp, err := c.request("GET", fmt.Sprintf("/context-resolve/v1/%s", uri), nil, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from context resolve: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var context connectpb.Context
	if err := json.Unmarshal(respBytes, &context); err != nil {
		return nil, fmt.Errorf("failed json unmarshalling Context: %w", err)
	}

	return &context, nil
}
