//go:build test_integration

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/require"
)

// testServer is a ConcreteApiServer on a random port with a stand-in for the
// daemon: reply decides what every request resolves to, and the requests it
// saw are readable afterwards. Built by hand rather than through NewApiServer
// because only the struct exposes the listener address.
type testServer struct {
	t      *testing.T
	url    string
	server *ConcreteApiServer

	received chan ApiRequest
}

func newTestServer(t *testing.T, reply func(req ApiRequest) (any, error)) *testServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &ConcreteApiServer{
		log:      &librespot.NullLogger{},
		listener: listener,
		requests: make(chan ApiRequest),
	}
	go s.serve()
	t.Cleanup(func() { _ = s.Close() })

	ts := &testServer{
		t:        t,
		url:      "http://" + listener.Addr().String(),
		server:   s,
		received: make(chan ApiRequest, 16),
	}

	// Stand in for AppPlayer.Run, which is what normally drains this channel.
	go func() {
		for req := range s.requests {
			ts.received <- req
			data, err := reply(req)
			req.Reply(data, err)
		}
	}()

	return ts
}

// do issues a request; body of nil sends no body at all, a string is sent verbatim.
func (ts *testServer) do(method, path string, body any) *http.Response {
	ts.t.Helper()

	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		require.NoError(ts.t, err)
		r = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, ts.url+path, r)
	require.NoError(ts.t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(ts.t, err)
	ts.t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

// request returns the single ApiRequest the daemon was handed, failing if none arrived.
func (ts *testServer) request() ApiRequest {
	ts.t.Helper()

	select {
	case req := <-ts.received:
		return req
	case <-time.After(2 * time.Second):
		ts.t.Fatal("no request reached the daemon")
		return ApiRequest{}
	}
}

// requireNoRequest asserts the handler rejected the call without bothering the daemon.
func (ts *testServer) requireNoRequest() {
	ts.t.Helper()

	select {
	case req := <-ts.received:
		ts.t.Fatalf("request %s should not have reached the daemon", req.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

func okReply(ApiRequest) (any, error) { return nil, nil }

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw)
}

// Every endpoint and the methods it accepts. Anything else must be refused
// before the daemon is involved.
var endpointMethods = map[string][]string{
	"/":                       {http.MethodGet},
	"/status":                 {http.MethodGet},
	"/auth/code":              {http.MethodGet},
	"/token":                  {http.MethodPost},
	"/set_device_name":        {http.MethodPost},
	"/player/play":            {http.MethodPost},
	"/player/resume":          {http.MethodPost},
	"/player/pause":           {http.MethodPost},
	"/player/playpause":       {http.MethodPost},
	"/player/stop":            {http.MethodPost},
	"/player/next":            {http.MethodPost},
	"/player/prev":            {http.MethodPost},
	"/player/seek":            {http.MethodPost},
	"/player/volume":          {http.MethodGet, http.MethodPost},
	"/player/repeat_context":  {http.MethodPost},
	"/player/repeat_track":    {http.MethodPost},
	"/player/shuffle_context": {http.MethodPost},
	"/player/add_to_queue":    {http.MethodPost},
	"/player/output":          {http.MethodPost},
}

func TestApiRejectsWrongMethod(t *testing.T) {
	ts := newTestServer(t, okReply)

	for path, allowed := range endpointMethods {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if slices.Contains(allowed, method) {
				continue
			}

			t.Run(method+path, func(t *testing.T) {
				resp := ts.do(method, path, nil)
				require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
			})
		}
	}
}

func TestApiRoot(t *testing.T) {
	ts := newTestServer(t, func(ApiRequest) (any, error) {
		return &ApiRoot{PlaybackReady: true}, nil
	})

	resp := ts.do(http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.JSONEq(t, `{"playback_ready":true}`, body(t, resp))

	require.Equal(t, ApiRequestTypeRoot, ts.request().Type)
}

// The pairing code is answered from the server's own state, never forwarded:
// the device authorization flow blocks the daemon before anything drains the
// request channel, so a request that had to reach a player would hang for as
// long as the code is worth having.
func TestApiAuthCode(t *testing.T) {
	ts := newTestServer(t, okReply)

	resp := ts.do(http.MethodGet, "/auth/code", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	ts.requireNoRequest()

	expiry := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	ts.server.SetAuthCode(&ApiDeviceAuth{
		Url:       "https://spotify.com/pair?code=ABCDEF",
		Code:      "ABCDEF",
		ExpiresAt: expiry,
	})

	resp = ts.do(http.MethodGet, "/auth/code", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.JSONEq(t, fmt.Sprintf(
		`{"url":"https://spotify.com/pair?code=ABCDEF","code":"ABCDEF","expires_at":%q}`,
		expiry.Format(time.RFC3339),
	), body(t, resp))
	ts.requireNoRequest()

	// Cleared once the user has approved the request or the code has expired.
	ts.server.SetAuthCode(nil)

	resp = ts.do(http.MethodGet, "/auth/code", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestApiStatus(t *testing.T) {
	ts := newTestServer(t, func(ApiRequest) (any, error) {
		return &ApiStatus{
			Username:    "someone",
			DeviceId:    "abc",
			DeviceName:  "test device",
			Volume:      42,
			VolumeSteps: 100,
			Stopped:     true,
		}, nil
	})

	resp := ts.do(http.MethodGet, "/status", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got ApiStatus
	require.NoError(t, json.Unmarshal([]byte(body(t, resp)), &got))
	require.Equal(t, "someone", got.Username)
	require.EqualValues(t, 42, got.Volume)
	require.True(t, got.Stopped)
	require.Nil(t, got.Track)

	require.Equal(t, ApiRequestTypeStatus, ts.request().Type)
}

// The status payload is the API's largest contract and is now generated from
// the spec, so pin the exact JSON: every field present, nothing omitted, and
// nulls where the schema says nullable.
func TestApiStatusWireFormat(t *testing.T) {
	coverUrl := "https://i.scdn.co/image/xxx"
	bitrate, sampleRate, bitDepth := 160, 44100, 16

	ts := newTestServer(t, func(ApiRequest) (any, error) {
		return &ApiStatus{
			Username:       "someone",
			DeviceId:       "abc",
			DeviceType:     "COMPUTER",
			DeviceName:     "test device",
			PlayOrigin:     "go-librespot",
			Stopped:        false,
			Paused:         false,
			Buffering:      false,
			Volume:         42,
			VolumeSteps:    100,
			RepeatContext:  false,
			RepeatTrack:    false,
			ShuffleContext: false,
			Track: &ApiTrack{
				Uri:           "spotify:track:xxx",
				Name:          "Some Song",
				ArtistNames:   []string{"Someone"},
				AlbumName:     "Some Album",
				AlbumCoverUrl: &coverUrl,
				Position:      1000,
				Duration:      200000,
				ReleaseDate:   "2020-01-01",
				TrackNumber:   3,
				DiscNumber:    1,
				Format:        "OGG_VORBIS_160",
				Codec:         TrackCodecVorbis,
				Bitrate:       &bitrate,
				SampleRate:    &sampleRate,
				BitDepth:      &bitDepth,
			},
		}, nil
	})

	resp := ts.do(http.MethodGet, "/status", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{
		"username": "someone",
		"device_id": "abc",
		"device_type": "COMPUTER",
		"device_name": "test device",
		"play_origin": "go-librespot",
		"stopped": false,
		"paused": false,
		"buffering": false,
		"volume": 42,
		"volume_steps": 100,
		"repeat_context": false,
		"repeat_track": false,
		"shuffle_context": false,
		"track": {
			"uri": "spotify:track:xxx",
			"name": "Some Song",
			"artist_names": ["Someone"],
			"album_name": "Some Album",
			"album_cover_url": "https://i.scdn.co/image/xxx",
			"position": 1000,
			"duration": 200000,
			"release_date": "2020-01-01",
			"track_number": 3,
			"disc_number": 1,
			"format": "OGG_VORBIS_160",
			"codec": "vorbis",
			"bitrate": 160,
			"sample_rate": 44100,
			"bit_depth": 16
		}
	}`, body(t, resp))
}

// The nullable fields must serialise as null rather than disappear.
func TestApiStatusWireFormatNulls(t *testing.T) {
	ts := newTestServer(t, func(ApiRequest) (any, error) {
		return &ApiStatus{Track: &ApiTrack{ArtistNames: []string{}}}, nil
	})

	resp := ts.do(http.MethodGet, "/status", nil)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body(t, resp)), &got))

	track, ok := got["track"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"album_cover_url", "bitrate", "sample_rate", "bit_depth"} {
		value, present := track[field]
		require.True(t, present, "%s must be present", field)
		require.Nil(t, value, "%s must be null", field)
	}
}

// The endpoints that take no body and just forward a bare request type.
func TestApiSimpleCommands(t *testing.T) {
	for path, want := range map[string]ApiRequestType{
		"/player/resume":    ApiRequestTypeResume,
		"/player/pause":     ApiRequestTypePause,
		"/player/playpause": ApiRequestTypePlayPause,
		"/player/stop":      ApiRequestTypeStop,
		"/player/prev":      ApiRequestTypePrev,
		"/token":            ApiRequestTypeToken,
	} {
		t.Run(path, func(t *testing.T) {
			ts := newTestServer(t, okReply)

			resp := ts.do(http.MethodPost, path, nil)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, want, ts.request().Type)
		})
	}
}

func TestApiPlay(t *testing.T) {
	t.Run("decodes the whole payload", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/play", map[string]any{
			"uri":         "spotify:playlist:xxx",
			"skip_to_uri": "spotify:track:yyy",
			"paused":      true,
			"position":    12345,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		req := ts.request()
		require.Equal(t, ApiRequestTypePlay, req.Type)
		require.Equal(t, ApiPlay{
			Uri:       "spotify:playlist:xxx",
			SkipToUri: "spotify:track:yyy",
			Paused:    true,
			Position:  12345,
		}, req.Data)
	})

	t.Run("position defaults to the start", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		ts.do(http.MethodPost, "/player/play", map[string]any{"uri": "spotify:track:xxx"})
		require.EqualValues(t, 0, ts.request().Data.(ApiPlay).Position)
	})

	for name, payload := range map[string]any{
		"missing uri":  map[string]any{"paused": true},
		"empty uri":    map[string]any{"uri": ""},
		"no body":      nil,
		"invalid json": "{not json",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			ts := newTestServer(t, okReply)

			resp := ts.do(http.MethodPost, "/player/play", payload)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			ts.requireNoRequest()
		})
	}
}

func TestApiNext(t *testing.T) {
	t.Run("with a target uri", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		ts.do(http.MethodPost, "/player/next", map[string]any{"uri": "spotify:track:xxx"})

		req := ts.request()
		require.Equal(t, ApiRequestTypeNext, req.Type)
		data := req.Data.(ApiNext)
		require.NotNil(t, data.Uri)
		require.Equal(t, "spotify:track:xxx", *data.Uri)
	})

	t.Run("without a body", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/next", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Nil(t, ts.request().Data.(ApiNext).Uri)
	})
}

func TestApiSeek(t *testing.T) {
	t.Run("absolute", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/seek", map[string]any{"position": 5000})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		req := ts.request()
		require.Equal(t, ApiRequestTypeSeek, req.Type)
		require.Equal(t, ApiSeek{Position: 5000}, req.Data)
	})

	t.Run("relative may go backwards", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/seek", map[string]any{"position": -5000, "relative": true})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, ApiSeek{Position: -5000, Relative: true}, ts.request().Data)
	})

	t.Run("absolute rejects a negative position", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/seek", map[string]any{"position": -1})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		ts.requireNoRequest()
	})
}

func TestApiVolume(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		ts := newTestServer(t, func(ApiRequest) (any, error) {
			return &ApiVolume{Value: 30, Max: 100}, nil
		})

		resp := ts.do(http.MethodGet, "/player/volume", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.JSONEq(t, `{"value":30,"max":100}`, body(t, resp))
		require.Equal(t, ApiRequestTypeGetVolume, ts.request().Type)
	})

	t.Run("set", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/volume", map[string]any{"volume": 64})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		req := ts.request()
		require.Equal(t, ApiRequestTypeSetVolume, req.Type)
		require.Equal(t, ApiSetVolume{Volume: 64}, req.Data)
	})

	t.Run("relative may go negative", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/volume", map[string]any{"volume": -10, "relative": true})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, ApiSetVolume{Volume: -10, Relative: true}, ts.request().Data)
	})

	t.Run("absolute rejects a negative volume", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/volume", map[string]any{"volume": -1})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		ts.requireNoRequest()
	})
}

// These three unwrap their single boolean field before handing it on.
func TestApiToggles(t *testing.T) {
	for _, tt := range []struct {
		path  string
		field string
		want  ApiRequestType
	}{
		{"/player/repeat_context", "repeat_context", ApiRequestTypeSetRepeatingContext},
		{"/player/repeat_track", "repeat_track", ApiRequestTypeSetRepeatingTrack},
		{"/player/shuffle_context", "shuffle_context", ApiRequestTypeSetShufflingContext},
	} {
		t.Run(tt.path, func(t *testing.T) {
			ts := newTestServer(t, okReply)

			resp := ts.do(http.MethodPost, tt.path, map[string]any{tt.field: true})
			require.Equal(t, http.StatusOK, resp.StatusCode)

			req := ts.request()
			require.Equal(t, tt.want, req.Type)
			require.Equal(t, true, req.Data)
		})

		t.Run(tt.path+" defaults to false", func(t *testing.T) {
			ts := newTestServer(t, okReply)

			ts.do(http.MethodPost, tt.path, map[string]any{})
			require.Equal(t, false, ts.request().Data)
		})
	}
}

func TestApiAddToQueue(t *testing.T) {
	t.Run("forwards the uri", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/add_to_queue", map[string]any{"uri": "spotify:track:xxx"})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		req := ts.request()
		require.Equal(t, ApiRequestTypeAddToQueue, req.Type)
		require.Equal(t, "spotify:track:xxx", req.Data)
	})

	t.Run("rejects an empty uri", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/add_to_queue", map[string]any{"uri": ""})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		ts.requireNoRequest()
	})
}

func TestApiSetDeviceName(t *testing.T) {
	t.Run("forwards the name", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/set_device_name", map[string]any{"name": "kitchen"})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		req := ts.request()
		require.Equal(t, ApiRequestSetDeviceName, req.Type)
		require.Equal(t, "kitchen", req.Data)
	})

	t.Run("rejects an empty name", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/set_device_name", map[string]any{"name": ""})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		ts.requireNoRequest()
	})
}

func TestApiReopenOutput(t *testing.T) {
	t.Run("forwards the device", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/output", map[string]any{"device": "hw:1,0"})
		require.Equal(t, http.StatusOK, resp.StatusCode)

		req := ts.request()
		require.Equal(t, ApiRequestTypeReopenOutput, req.Type)
		require.Equal(t, "hw:1,0", req.Data)
	})

	// An empty device is meaningful here: it selects the configured default.
	t.Run("accepts an empty device", func(t *testing.T) {
		ts := newTestServer(t, okReply)

		resp := ts.do(http.MethodPost, "/player/output", map[string]any{"device": ""})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "", ts.request().Data)
	})
}

func TestApiErrorsMapToStatusCodes(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{ErrNoSession, http.StatusNoContent},
		{ErrForbidden, http.StatusForbidden},
		{ErrNotFound, http.StatusNotFound},
		{ErrMethodNotAllowed, http.StatusMethodNotAllowed},
		{ErrTooManyRequests, http.StatusTooManyRequests},
		{ErrBadRequest, http.StatusBadRequest},
		{errors.New("something else"), http.StatusInternalServerError},
	} {
		t.Run(fmt.Sprint(tt.want), func(t *testing.T) {
			ts := newTestServer(t, func(ApiRequest) (any, error) { return nil, tt.err })

			resp := ts.do(http.MethodGet, "/status", nil)
			require.Equal(t, tt.want, resp.StatusCode)
		})
	}
}

// Errors are matched with errors.Is, so a wrapped sentinel must map the same way.
func TestApiWrappedErrorsMapToStatusCodes(t *testing.T) {
	ts := newTestServer(t, func(ApiRequest) (any, error) {
		return nil, fmt.Errorf("while doing the thing: %w", ErrNotFound)
	})

	resp := ts.do(http.MethodGet, "/status", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestApiUnknownPathIsNotFound(t *testing.T) {
	ts := newTestServer(t, okReply)

	// The generated router registers the root as "GET /{$}", an exact match, so
	// unrouted paths 404. Before codegen "/" was a plain ServeMux catch-all and
	// this answered 200 with the root payload.
	resp := ts.do(http.MethodGet, "/nope", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	ts.requireNoRequest()
}

func TestApiEventsWebsocketReceivesEmittedEvents(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &ConcreteApiServer{
		log:      &librespot.NullLogger{},
		listener: listener,
		requests: make(chan ApiRequest),
	}
	go s.serve()
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws://"+listener.Addr().String()+"/events", nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Emit only reaches clients already registered, and registration finishes
	// asynchronously after the handshake returns.
	require.Eventually(t, func() bool {
		s.clientsLock.RLock()
		defer s.clientsLock.RUnlock()
		return len(s.clients) == 1
	}, 2*time.Second, 10*time.Millisecond)

	s.Emit(&ApiEvent{Type: ApiEventTypeVolume, Data: ApiEventDataVolume{Value: 55, Max: 100}})

	_, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"volume","data":{"value":55,"max":100}}`, string(raw))
}
