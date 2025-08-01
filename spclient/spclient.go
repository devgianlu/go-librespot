package spclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	eventsenderpb "github.com/devgianlu/go-librespot/proto/spotify/event_sender"
	netfortunepb "github.com/devgianlu/go-librespot/proto/spotify/netfortune"
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
	"google.golang.org/protobuf/proto"
)

type Spclient struct {
	log librespot.Logger

	client *http.Client

	baseUrl     *url.URL
	clientToken string
	deviceId    string

	accessToken librespot.GetLogin5TokenFunc
}

func NewSpclient(ctx context.Context, log librespot.Logger, client *http.Client, addr librespot.GetAddressFunc, accessToken librespot.GetLogin5TokenFunc, deviceId, clientToken string) (*Spclient, error) {
	baseUrl, err := url.Parse(fmt.Sprintf("https://%s/", addr(ctx)))
	if err != nil {
		return nil, fmt.Errorf("invalid spclient base url: %w", err)
	}

	return &Spclient{
		log:         log,
		client:      client,
		baseUrl:     baseUrl,
		clientToken: clientToken,
		deviceId:    deviceId,
		accessToken: accessToken,
	}, nil
}

func (c *Spclient) innerRequest(ctx context.Context, method string, reqUrl *url.URL, query url.Values, header http.Header, body []byte) (*http.Response, error) {
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
		req.Header.Set("Content-Type", "application/x-protobuf")

		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.Body, _ = req.GetBody()
	}

	var forceNewToken bool
	resp, err := backoff.RetryWithData(func() (*http.Response, error) {
		accessToken, err := c.accessToken(ctx, forceNewToken)
		if err != nil {
			// Fail with a permanent error if we can't get a new token. The caller should have already retried, there's
			// nothing we can do.
			return nil, backoff.Permanent(fmt.Errorf("failed obtaining spclient access token: %w", err))
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

		resp, err := c.client.Do(req.WithContext(ctx))
		if err != nil {
			return nil, err
		} else if resp.StatusCode == 401 {
			forceNewToken = true
			return nil, fmt.Errorf("unauthorized")
		}

		return resp, nil
	}, backoff.WithContext(backoff.NewExponentialBackOff(), ctx))
	if err != nil {
		return nil, fmt.Errorf("spclient request failed: %w", err)
	}

	return resp, nil
}

func (c *Spclient) WebApiRequest(ctx context.Context, method string, path string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	reqPath, err := url.Parse("https://api.spotify.com/")
	if err != nil {
		panic("invalid api base url")
	}
	reqURL := reqPath.JoinPath(path)
	return c.innerRequest(ctx, method, reqURL, query, header, body)
}

func (c *Spclient) Request(ctx context.Context, method string, path string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	reqUrl := c.baseUrl.JoinPath(path)
	return c.innerRequest(ctx, method, reqUrl, query, header, body)
}

type putStateError struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func (c *Spclient) PutConnectStateInactive(ctx context.Context, spotConnId string, notify bool) error {
	resp, err := c.Request(
		ctx,
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
		c.log.Debug("put connect state inactive")
		return nil
	}
}

func (c *Spclient) PutConnectState(ctx context.Context, spotConnId string, reqProto *connectpb.PutStateRequest) error {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return fmt.Errorf("failed marshalling PutStateRequest: %w", err)
	}
	_, err = backoff.RetryWithData(func() (*http.Response, error) {
		resp, err := c.Request(
			ctx,
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
				c.log.Debugf("failed reading error response %s", err)
				return nil, fmt.Errorf("failed reading error response: %w", err)
			}
			c.log.Debugf("put state request failed with status %d: %s", resp.StatusCode, putError.Message)
			return nil, fmt.Errorf("put state request failed with status %d: %s", resp.StatusCode, putError.Message)
		} else {
			c.log.Debugf("put connect state because %s", reqProto.PutStateReason)
			return resp, nil
		}
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewConstantBackOff(1*time.Second), 2), ctx))
	if err != nil {
		return err
	}
	return nil
}

func (c *Spclient) ResolveStorageInteractive(ctx context.Context, fileId []byte, prefetch bool) (*storagepb.StorageResolveResponse, error) {
	var path string
	if prefetch {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive_prefetch/%s", hex.EncodeToString(fileId))
	} else {
		path = fmt.Sprintf("/storage-resolve/files/audio/interactive/%s", hex.EncodeToString(fileId))
	}

	resp, err := c.Request(ctx, "GET", path, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 503 {
		c.log.Debugf("storage resolve returned service unavailable, retrying...")
		_ = resp.Body.Close()

		resp, err = c.Request(ctx, "GET", path, nil, nil, nil)
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

func (c *Spclient) MetadataForTrack(ctx context.Context, track librespot.SpotifyId) (*metadatapb.Track, error) {
	if track.Type() != librespot.SpotifyIdTypeTrack {
		panic(fmt.Sprintf("invalid type: %s", track.Type()))
	}

	resp, err := c.Request(ctx, "GET", fmt.Sprintf("/metadata/4/track/%s", track.Hex()), nil, nil, nil)
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

func (c *Spclient) MetadataForEpisode(ctx context.Context, episode librespot.SpotifyId) (*metadatapb.Episode, error) {
	if episode.Type() != librespot.SpotifyIdTypeEpisode {
		panic(fmt.Sprintf("invalid type: %s", episode.Type()))
	}

	resp, err := c.Request(ctx, "GET", fmt.Sprintf("/metadata/4/episode/%s", episode.Hex()), nil, nil, nil)
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

func (c *Spclient) PlaylistSignals(ctx context.Context, playlist librespot.SpotifyId, reqProto *playlist4pb.ListSignals, lenses []string) (*playlist4pb.SelectedListContent, error) {
	if playlist.Type() != librespot.SpotifyIdTypePlaylist {
		panic(fmt.Sprintf("invalid type: %s", playlist.Type()))
	}

	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling ListSignals: %w", err)
	}

	resp, err := c.Request(ctx, "POST", fmt.Sprintf("/playlist/v2/playlist/%s/signals", playlist.Base62()), nil, http.Header{
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

func (c *Spclient) ContextResolve(ctx context.Context, uri string) (*connectpb.Context, error) {
	resp, err := c.Request(ctx, "GET", fmt.Sprintf("/context-resolve/v1/%s", uri), nil, nil, nil)
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

func (c *Spclient) ContextResolveAutoplay(ctx context.Context, reqProto *playerpb.AutoplayContextRequest) (*connectpb.Context, error) {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling AutoplayContextRequest: %w", err)
	}

	resp, err := c.Request(ctx, "POST", "/context-resolve/v1/autoplay", nil, nil, reqBody)
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

func (c *Spclient) GetAccessToken(ctx context.Context, force bool) (string, error) {
	return c.accessToken(ctx, force)
}

func (c *Spclient) NetFortune(ctx context.Context, bandwidth int) (*netfortunepb.NetFortuneV2Response, error) {
	resp, err := c.Request(ctx, "GET", "/net-fortune/v2/fortune", url.Values{
		"bandwidth": []string{fmt.Sprintf("%d", bandwidth)},
	}, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from net fortune: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var fortune netfortunepb.NetFortuneV2Response
	if err := proto.Unmarshal(respBytes, &fortune); err != nil {
		return nil, fmt.Errorf("failed json unmarshalling NetFortuneV2Response: %w", err)
	}

	return &fortune, nil
}

func (c *Spclient) PublishEvents(ctx context.Context, reqProto *eventsenderpb.PublishEventsRequest) (*eventsenderpb.PublishEventsResponse, error) {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling PublishEventsRequest: %w", err)
	}

	resp, err := c.Request(ctx, "POST", "/gabo-receiver-service/v3/events/", nil, nil, reqBody)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from gabo events receiver: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var respProto eventsenderpb.PublishEventsResponse
	if err := proto.Unmarshal(respBytes, &respProto); err != nil {
		return nil, fmt.Errorf("failed json unmarshalling PublishEventsResponse: %w", err)
	}

	return &respProto, nil
}
