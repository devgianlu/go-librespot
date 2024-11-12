package spclient

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	storagepb "github.com/devgianlu/go-librespot/proto/spotify/download"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	playerpb "github.com/devgianlu/go-librespot/proto/spotify/player"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
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

func (c *Spclient) innerRequest(method string, reqUrl *url.URL, query url.Values, header http.Header, body []byte) (*http.Response, error) {
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

	if body != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.Body, _ = req.GetBody()
	}

	var forceNewToken bool
	resp, err := backoff.RetryWithData(func() (*http.Response, error) {
		accessToken, err := c.accessToken(forceNewToken)
		if err != nil {
			return nil, fmt.Errorf("failed obtaining spclient access token: %w", err)
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		} else if resp.StatusCode == 401 {
			forceNewToken = true
			return nil, fmt.Errorf("unauthorized")
		}

		return resp, nil
	}, backoff.NewExponentialBackOff())
	if err != nil {
		return nil, fmt.Errorf("spclient request failed: %w", err)
	}

	return resp, nil
}

func (c *Spclient) WebApiRequest(method string, path string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	reqPath, err := url.Parse("https://api.spotify.com/")
	if err != nil {
		panic("invalid api base url")
	}
	reqURL := reqPath.JoinPath(path)
	return c.innerRequest(method, reqURL, query, header, body)
}

func (c *Spclient) Request(method string, path string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	reqUrl := c.baseUrl.JoinPath(path)
	return c.innerRequest(method, reqUrl, query, header, body)
}

type putStateError struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func (c *Spclient) PutConnectStateInactive(spotConnId string, notify bool) error {
	resp, err := c.Request(
		"PUT",
		fmt.Sprintf("/connect-state/v1/devices/%s/inactive", c.deviceId),
		url.Values{"notify": []string{strconv.FormatBool(notify)}},
		http.Header{
			"X-Spotify-Connection-Id": []string{spotConnId},
		},
		nil,
	)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 204 {
		return fmt.Errorf("put state inactive request failed with status %d", resp.StatusCode)
	} else {
		log.Debug("put connect state inactive")
		return nil
	}
}

func (c *Spclient) PutConnectState(spotConnId string, reqProto *connectpb.PutStateRequest) error {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return fmt.Errorf("failed marshalling PutStateRequest: %w", err)
	}
	_, err = backoff.RetryWithData(func() (*http.Response, error) {
		resp, err := c.Request(
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
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != 200 {
			var putError putStateError
			if err := json.NewDecoder(resp.Body).Decode(&putError); err != nil {
				log.Debugf("failed reading error response %s", err)
				return nil, fmt.Errorf("failed reading error response: %w", err)
			}
			log.Debugf("put state request failed with status %d: %s", resp.StatusCode, putError.Message)
			return nil, fmt.Errorf("put state request failed with status %d: %s", resp.StatusCode, putError.Message)
		} else {
			log.Debugf("put connect state because %s", reqProto.PutStateReason)
			return resp, nil
		}
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(1*time.Second), 2))
	if err != nil {
		return err
	}
	return nil
}

func (c *Spclient) ResolveStorageInteractive(fileId []byte, prefetch bool) (*storagepb.StorageResolveResponse, error) {
	var path string
	if prefetch {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive_prefetch/%s", hex.EncodeToString(fileId))
	} else {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive/%s", hex.EncodeToString(fileId))
	}

	resp, err := c.Request("GET", path, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 503 {
		log.Debugf("storage resolve returned service unavailable, retrying...")
		_ = resp.Body.Close()

		resp, err = c.Request("GET", path, nil, nil, nil)
		if err != nil {
			return nil, err
		}
	}

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

func (c *Spclient) MetadataForTrack(track librespot.SpotifyId) (*metadatapb.Track, error) {
	if track.Type() != librespot.SpotifyIdTypeTrack {
		panic(fmt.Sprintf("invalid type: %s", track.Type()))
	}

	resp, err := c.Request("GET", fmt.Sprintf("/metadata/4/track/%s", track.Hex()), nil, nil, nil)
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

func (c *Spclient) MetadataForEpisode(episode librespot.SpotifyId) (*metadatapb.Episode, error) {
	if episode.Type() != librespot.SpotifyIdTypeEpisode {
		panic(fmt.Sprintf("invalid type: %s", episode.Type()))
	}

	resp, err := c.Request("GET", fmt.Sprintf("/metadata/4/episode/%s", episode.Hex()), nil, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from episode metadata: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp metadatapb.Episode
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling Episode: %w", err)
	}

	return &protoResp, nil
}

func (c *Spclient) PlaylistSignals(playlist librespot.SpotifyId, reqProto *playlist4pb.ListSignals, lenses []string) (*playlist4pb.SelectedListContent, error) {
	if playlist.Type() != librespot.SpotifyIdTypePlaylist {
		panic(fmt.Sprintf("invalid type: %s", playlist.Type()))
	}

	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling ListSignals: %w", err)
	}

	resp, err := c.Request("POST", fmt.Sprintf("/playlist/v2/playlist/%s/signals", playlist.Base62()), nil, http.Header{
		"Spotify-Apply-Lenses": lenses,
	}, reqBody)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from playlist signals: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp playlist4pb.SelectedListContent
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling SelectedListContent: %w", err)
	}

	return &protoResp, nil
}

func (c *Spclient) ContextResolve(uri string) (*connectpb.Context, error) {
	resp, err := c.Request("GET", fmt.Sprintf("/context-resolve/v1/%s", uri), nil, nil, nil)
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

func (c *Spclient) ContextResolveAutoplay(reqProto *playerpb.AutoplayContextRequest) (*connectpb.Context, error) {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling AutoplayContextRequest: %w", err)
	}

	resp, err := c.Request("POST", "/context-resolve/v1/autoplay", nil, nil, reqBody)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from context resolve autoplay: %d", resp.StatusCode)
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

func (c *Spclient) GetAccessToken(force bool) (string, error) {
	return c.accessToken(force)
}
