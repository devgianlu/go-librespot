package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/rs/cors"
)

const timeout = 10 * time.Second

type ApiServer interface {
	Emit(ev *ApiEvent)
	Receive() <-chan ApiRequest
	SetAuthCode(auth *ApiDeviceAuth)
	Close() error
}

type ConcreteApiServer struct {
	log librespot.Logger

	allowOrigin string
	certFile    string
	keyFile     string

	// close is read by the websocket loop and by serve while Close writes it
	// from whichever goroutine shuts the daemon down.
	close    atomic.Bool
	listener net.Listener

	requests chan ApiRequest

	// authCode is the pending device authorization pairing code.
	authCode atomic.Pointer[ApiDeviceAuth]

	clients     []*websocket.Conn
	clientsLock sync.RWMutex
}

var (
	ErrNoSession        = errors.New("no session")
	ErrBadRequest       = errors.New("bad request")
	ErrForbidden        = errors.New("forbidden")
	ErrNotFound         = errors.New("not found")
	ErrMethodNotAllowed = errors.New("method not allowed")
	ErrTooManyRequests  = errors.New("the app has exceeded its rate limits")
)

type ApiRequestType string

const (
	ApiRequestTypeRoot                ApiRequestType = "root"
	ApiRequestTypeStatus              ApiRequestType = "status"
	ApiRequestTypeResume              ApiRequestType = "resume"
	ApiRequestTypePause               ApiRequestType = "pause"
	ApiRequestTypePlayPause           ApiRequestType = "playpause"
	ApiRequestTypeSeek                ApiRequestType = "seek"
	ApiRequestTypePrev                ApiRequestType = "prev"
	ApiRequestTypeNext                ApiRequestType = "next"
	ApiRequestTypePlay                ApiRequestType = "play"
	ApiRequestTypeStop                ApiRequestType = "stop"
	ApiRequestTypeGetVolume           ApiRequestType = "get_volume"
	ApiRequestTypeSetVolume           ApiRequestType = "set_volume"
	ApiRequestTypeSetRepeatingContext ApiRequestType = "repeating_context"
	ApiRequestTypeSetRepeatingTrack   ApiRequestType = "repeating_track"
	ApiRequestTypeSetShufflingContext ApiRequestType = "shuffling_context"
	ApiRequestTypeAddToQueue          ApiRequestType = "add_to_queue"
	ApiRequestTypeToken               ApiRequestType = "token"
	ApiRequestSetDeviceName           ApiRequestType = "set_device_name"
	ApiRequestTypeReopenOutput        ApiRequestType = "reopen_output"
)

type ApiEventType string

const (
	ApiEventTypePlaying        ApiEventType = "playing"
	ApiEventTypeNotPlaying     ApiEventType = "not_playing"
	ApiEventTypeWillPlay       ApiEventType = "will_play"
	ApiEventTypePaused         ApiEventType = "paused"
	ApiEventTypeActive         ApiEventType = "active"
	ApiEventTypeInactive       ApiEventType = "inactive"
	ApiEventTypeMetadata       ApiEventType = "metadata"
	ApiEventTypeVolume         ApiEventType = "volume"
	ApiEventTypeSeek           ApiEventType = "seek"
	ApiEventTypeStopped        ApiEventType = "stopped"
	ApiEventTypeRepeatTrack    ApiEventType = "repeat_track"
	ApiEventTypeRepeatContext  ApiEventType = "repeat_context"
	ApiEventTypeShuffleContext ApiEventType = "shuffle_context"
	ApiEventTypePlaybackReady  ApiEventType = "playback_ready"
)

type ApiRequest struct {
	Type ApiRequestType
	Data any

	resp chan apiResponse
}

func (r *ApiRequest) Reply(data any, err error) {
	r.resp <- apiResponse{data, err}
}

// NewApiRequest builds an ApiRequest pre-wired with a reply channel, plus a
// wait function that blocks until the daemon calls Reply (or ctx is done).
func NewApiRequest(t ApiRequestType, data any) (req ApiRequest, wait func(context.Context) (any, error)) {
	ch := make(chan apiResponse, 1)
	req = ApiRequest{Type: t, Data: data, resp: ch}
	wait = func(ctx context.Context) (any, error) {
		select {
		case r := <-ch:
			return r.data, r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return
}

type apiResponse struct {
	data any
	err  error
}

func getBestImageIdForSize(images []*metadatapb.Image, size string) []byte {
	if len(images) == 0 {
		return nil
	}

	imageSize := metadatapb.Image_Size(metadatapb.Image_Size_value[strings.ToUpper(size)])

	dist := func(a metadatapb.Image_Size) int {
		diff := int(a) - int(imageSize)
		if diff < 0 {
			return -diff
		}
		return diff
	}

	// Find an image with the exact requested size.
	// If no exact match, return the closest image to the requested size.
	var bestImage *metadatapb.Image
	for _, img := range images {
		if img.Size == nil {
			continue
		}

		if *img.Size == imageSize {
			return img.FileId
		}

		// Find the image with the closest size. This logic works because the
		// metadatapb.Image_Size enum values are ordered from smallest to largest.
		if bestImage == nil || dist(*img.Size) < dist(*bestImage.Size) {
			bestImage = img
		}
	}

	if bestImage != nil {
		return bestImage.FileId
	}

	// Fallback to the first image if none have size information.
	return images[0].FileId
}

func (p *AppPlayer) newApiResponseStatusTrack(stream *player.Stream, position int64) *ApiTrack {
	media := stream.Media

	resp := p.newApiResponseStatusMedia(media, position)

	// The file is what actually got decoded, so report from it rather than from
	// the requested bitrate: they differ whenever the preferred format was not
	// on offer.
	if stream.File != nil && stream.File.Format != nil {
		format := *stream.File.Format
		resp.Format = format.String()
		resp.Codec = TrackCodec(player.GetFormatCodec(format))
		if bitrate := player.GetFormatBitrate(format); bitrate > 0 {
			resp.Bitrate = &bitrate
		}
	}

	// Taken from the decoder rather than the format name so they stay honest if
	// the player ever stops requiring 44100Hz.
	if stream.SampleRate > 0 {
		sampleRate := int(stream.SampleRate)
		resp.SampleRate = &sampleRate
	}
	if stream.BitDepth > 0 {
		bitDepth := int(stream.BitDepth)
		resp.BitDepth = &bitDepth
	}

	return resp
}

func (p *AppPlayer) newApiResponseStatusMedia(media *librespot.Media, position int64) *ApiTrack {
	if media.IsTrack() {
		track := media.Track()

		var artists []string
		for _, a := range track.Artist {
			artists = append(artists, *a.Name)
		}

		albumCoverId := getBestImageIdForSize(track.Album.Cover, p.app.cfg.ImageSize)
		if albumCoverId == nil && track.Album.CoverGroup != nil {
			albumCoverId = getBestImageIdForSize(track.Album.CoverGroup.Image, p.app.cfg.ImageSize)
		}

		return &ApiTrack{
			Uri:           librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, track.Gid).Uri(),
			Name:          *track.Name,
			ArtistNames:   artists,
			AlbumName:     *track.Album.Name,
			AlbumCoverUrl: p.prodInfo.ImageUrl(albumCoverId),
			Position:      position,
			Duration:      int(*track.Duration),
			ReleaseDate:   track.Album.Date.String(),
			TrackNumber:   int(*track.Number),
			DiscNumber:    int(*track.DiscNumber),
		}
	} else {
		episode := media.Episode()

		albumCoverId := getBestImageIdForSize(episode.CoverImage.Image, p.app.cfg.ImageSize)

		return &ApiTrack{
			Uri:           librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeEpisode, episode.Gid).Uri(),
			Name:          *episode.Name,
			ArtistNames:   []string{*episode.Show.Name},
			AlbumName:     *episode.Show.Name,
			AlbumCoverUrl: p.prodInfo.ImageUrl(albumCoverId),
			Position:      position,
			Duration:      int(*episode.Duration),
			ReleaseDate:   "",
			TrackNumber:   0,
			DiscNumber:    0,
		}
	}
}

type ApiEvent struct {
	Type ApiEventType `json:"type"`
	Data any          `json:"data"`
}

type ApiEventDataMetadata ApiTrack

type ApiEventDataVolume ApiVolume

type ApiEventDataPlaying struct {
	ContextUri string `json:"context_uri"`
	Uri        string `json:"uri"`
	Resume     bool   `json:"resume"`
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataWillPlay struct {
	ContextUri string `json:"context_uri"`
	Uri        string `json:"uri"`
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataNotPlaying struct {
	ContextUri string `json:"context_uri"`
	Uri        string `json:"uri"`
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataPaused struct {
	ContextUri string `json:"context_uri"`
	Uri        string `json:"uri"`
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataStopped struct {
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataSeek struct {
	ContextUri string `json:"context_uri"`
	Uri        string `json:"uri"`
	Position   int    `json:"position"`
	Duration   int    `json:"duration"`
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataRepeatTrack struct {
	Value bool `json:"value"`
}

type ApiEventDataRepeatContext struct {
	Value bool `json:"value"`
}

type ApiEventDataShuffleContext struct {
	Value bool `json:"value"`
}

func NewApiServer(log librespot.Logger, address string, port int, allowOrigin string, certFile string, keyFile string) (_ ApiServer, err error) {
	s := &ConcreteApiServer{log: log, allowOrigin: allowOrigin, certFile: certFile, keyFile: keyFile}
	s.requests = make(chan ApiRequest)

	s.listener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", address, port))
	if err != nil {
		return nil, fmt.Errorf("failed starting api listener: %w", err)
	}

	log.Infof("api server listening on %s", s.listener.Addr())

	go s.serve()
	return s, nil
}

type StubApiServer struct {
	log librespot.Logger
}

func NewStubApiServer(log librespot.Logger) (ApiServer, error) {
	return &StubApiServer{log: log}, nil
}

func (s *StubApiServer) Emit(ev *ApiEvent) {
	s.log.Tracef("voiding websocket event: %s", ev.Type)
}

func (s *StubApiServer) Receive() <-chan ApiRequest {
	return make(<-chan ApiRequest)
}

func (s *StubApiServer) SetAuthCode(*ApiDeviceAuth) {}

func (s *StubApiServer) Close() error {
	return nil
}

func (s *ConcreteApiServer) handleRequest(req ApiRequest, w http.ResponseWriter) {
	req.resp = make(chan apiResponse, 1)
	s.requests <- req
	resp := <-req.resp

	if resp.err != nil {
		switch {
		case errors.Is(resp.err, ErrNoSession):
			w.WriteHeader(http.StatusNoContent)
			return
		case errors.Is(resp.err, ErrForbidden):
			w.WriteHeader(http.StatusForbidden)
			return
		case errors.Is(resp.err, ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
			return
		case errors.Is(resp.err, ErrMethodNotAllowed):
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		case errors.Is(resp.err, ErrTooManyRequests):
			w.WriteHeader(http.StatusTooManyRequests)
			return
		case errors.Is(resp.err, ErrBadRequest):
			w.WriteHeader(http.StatusBadRequest)
			return
		default:
			s.log.WithError(resp.err).Errorf("failed handling request %s", req.Type)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	switch respData := resp.data.(type) {
	case []byte:
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(respData)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respData)
	}
}

func jsonDecode(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	} else if len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, v)
}

// The handlers below implement the generated ServerInterface; the routing that
// dispatches to them is generated from api-spec.yml into api_gen.go. Each one
// decodes and validates its payload, then hands an ApiRequest to the daemon and
// blocks on the reply.

var _ ServerInterface = (*ConcreteApiServer)(nil)

func (s *ConcreteApiServer) GetRoot(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypeRoot}, w)
}

func (s *ConcreteApiServer) GetStatus(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypeStatus}, w)
}

func (s *ConcreteApiServer) GetAuthCode(w http.ResponseWriter, _ *http.Request) {
	auth := s.authCode.Load()
	if auth == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(auth)
}

func (s *ConcreteApiServer) GetToken(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypeToken}, w)
}

func (s *ConcreteApiServer) PlayerResume(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypeResume}, w)
}

func (s *ConcreteApiServer) PlayerPause(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypePause}, w)
}

func (s *ConcreteApiServer) PlayerPlayPause(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypePlayPause}, w)
}

func (s *ConcreteApiServer) PlayerStop(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypeStop}, w)
}

func (s *ConcreteApiServer) PlayerPrev(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypePrev}, w)
}

func (s *ConcreteApiServer) PlayerGetVolume(w http.ResponseWriter, _ *http.Request) {
	s.handleRequest(ApiRequest{Type: ApiRequestTypeGetVolume}, w)
}

func (s *ConcreteApiServer) PlayerPlay(w http.ResponseWriter, r *http.Request) {
	var data ApiPlay
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if len(data.Uri) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypePlay, Data: data}, w)
}

func (s *ConcreteApiServer) PlayerNext(w http.ResponseWriter, r *http.Request) {
	var data ApiNext
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeNext, Data: data}, w)
}

func (s *ConcreteApiServer) PlayerSeek(w http.ResponseWriter, r *http.Request) {
	var data ApiSeek
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !data.Relative && data.Position < 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeSeek, Data: data}, w)
}

func (s *ConcreteApiServer) PlayerSetVolume(w http.ResponseWriter, r *http.Request) {
	var data ApiSetVolume
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !data.Relative && data.Volume < 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeSetVolume, Data: data}, w)
}

func (s *ConcreteApiServer) PlayerRepeatContext(w http.ResponseWriter, r *http.Request) {
	var data ApiRepeatContext
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeSetRepeatingContext, Data: data.RepeatContext}, w)
}

func (s *ConcreteApiServer) PlayerRepeatTrack(w http.ResponseWriter, r *http.Request) {
	var data ApiRepeatTrack
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeSetRepeatingTrack, Data: data.RepeatTrack}, w)
}

func (s *ConcreteApiServer) PlayerShuffleContext(w http.ResponseWriter, r *http.Request) {
	var data ApiShuffleContext
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeSetShufflingContext, Data: data.ShuffleContext}, w)
}

func (s *ConcreteApiServer) PlayerAddToQueue(w http.ResponseWriter, r *http.Request) {
	var data ApiAddToQueue
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if len(data.Uri) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeAddToQueue, Data: data.Uri}, w)
}

func (s *ConcreteApiServer) SetDeviceName(w http.ResponseWriter, r *http.Request) {
	var data ApiSetDeviceName
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if len(data.Name) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestSetDeviceName, Data: data.Name}, w)
}

func (s *ConcreteApiServer) PlayerOutput(w http.ResponseWriter, r *http.Request) {
	var data ApiOutput
	if err := jsonDecode(r, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s.handleRequest(ApiRequest{Type: ApiRequestTypeReopenOutput, Data: data.Device}, w)
}

func (s *ConcreteApiServer) GetEvents(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{}
	if len(s.allowOrigin) > 0 {
		allow := s.allowOrigin
		allow = strings.TrimPrefix(allow, "http://")
		allow = strings.TrimPrefix(allow, "https://")
		allow = strings.TrimSuffix(allow, "/")
		opts.OriginPatterns = []string{allow}
	}

	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		s.log.WithError(err).Error("failed accepting websocket connection")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// add the client to the list
	s.clientsLock.Lock()
	s.clients = append(s.clients, c)
	s.clientsLock.Unlock()

	s.log.Debugf("new websocket client")

	for {
		_, _, err := c.Read(context.Background())
		if s.close.Load() {
			return
		} else if err != nil {
			s.log.WithError(err).Error("websocket connection errored")

			// remove the client from the list
			s.clientsLock.Lock()
			for i, cc := range s.clients {
				if cc == c {
					s.clients = append(s.clients[:i], s.clients[i+1:]...)
					break
				}
			}
			s.clientsLock.Unlock()
			return
		}
	}
}

func (s *ConcreteApiServer) serve() {
	m := http.NewServeMux()
	handler := HandlerFromMux(s, m)

	c := cors.New(cors.Options{
		AllowedOrigins:      []string{s.allowOrigin},
		AllowPrivateNetwork: true,
		AllowCredentials:    true,
	})

	var err error
	if len(s.certFile) > 0 && len(s.keyFile) > 0 {
		err = http.ServeTLS(s.listener, c.Handler(handler), s.certFile, s.keyFile)
	} else {
		err = http.Serve(s.listener, c.Handler(handler))
	}

	if s.close.Load() {
		return
	} else if err != nil {
		s.log.WithError(err).Error("failed serving api")
		_ = s.Close()
	}
}
func (s *ConcreteApiServer) Emit(ev *ApiEvent) {
	s.clientsLock.RLock()
	defer s.clientsLock.RUnlock()

	s.log.Tracef("emitting websocket event: %s", ev.Type)

	for _, client := range s.clients {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := wsjson.Write(ctx, client, ev)
		cancel()
		if err != nil {
			// purposely do not propagate this to the caller
			s.log.WithError(err).Error("failed communicating with websocket client")
		}
	}
}

func (s *ConcreteApiServer) Receive() <-chan ApiRequest {
	return s.requests
}

func (s *ConcreteApiServer) SetAuthCode(auth *ApiDeviceAuth) {
	s.authCode.Store(auth)
}

func (s *ConcreteApiServer) Close() error {
	s.close.Store(true)

	// close all websocket clients
	s.clientsLock.RLock()
	for _, client := range s.clients {
		_ = client.Close(websocket.StatusGoingAway, "")
	}
	s.clientsLock.RUnlock()

	// close the listener
	_ = s.listener.Close()
	return nil
}
