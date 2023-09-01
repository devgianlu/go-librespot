package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	metadatapb "go-librespot/proto/spotify/metadata"
	"net"
	"net/http"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
	"sync"
)

type ApiServer struct {
	close    bool
	listener net.Listener

	requests chan ApiRequest

	clients     []*websocket.Conn
	clientsLock sync.RWMutex
}

var ErrNoSession = errors.New("no session")

type ApiRequestType string

const (
	ApiRequestTypeStatus    ApiRequestType = "status"
	ApiRequestTypeResume    ApiRequestType = "resume"
	ApiRequestTypePause     ApiRequestType = "pause"
	ApiRequestTypeSeek      ApiRequestType = "seek"
	ApiRequestTypePrev      ApiRequestType = "prev"
	ApiRequestTypeNext      ApiRequestType = "next"
	ApiRequestTypePlay      ApiRequestType = "play"
	ApiRequestTypeGetVolume ApiRequestType = "get_volume"
	ApiRequestTypeSetVolume ApiRequestType = "set_volume"
)

type ApiEventType string

const (
	ApiEventTypePlaying    ApiEventType = "playing"
	ApiEventTypeNotPlaying ApiEventType = "not_playing"
	ApiEventTypePaused     ApiEventType = "paused"
	ApiEventTypeActive     ApiEventType = "active"
	ApiEventTypeInactive   ApiEventType = "inactive"
	ApiEventTypeTrack      ApiEventType = "track"
	ApiEventTypeVolume     ApiEventType = "volume"
	ApiEventTypeSeek       ApiEventType = "seek"
	ApiEventTypeStopped    ApiEventType = "stopped"
)

type ApiRequest struct {
	Type ApiRequestType
	Data any

	resp chan apiResponse
}

func (r *ApiRequest) Reply(data any, err error) {
	r.resp <- apiResponse{data, err}
}

type ApiRequestDataPlay struct {
	Uri       string `json:"uri"`
	SkipToUri string `json:"skip_to_uri"`
	Paused    bool   `json:"paused"`
}

type apiResponse struct {
	data any
	err  error
}

type ApiResponseStatusTrack struct {
	Uri           string   `json:"uri"`
	Name          string   `json:"name"`
	ArtistNames   []string `json:"artist_names"`
	AlbumName     string   `json:"album_name"`
	AlbumCoverUrl string   `json:"album_cover_url"`
	Position      int      `json:"position"`
	Duration      int      `json:"duration"`
}

func NewApiResponseStatusTrack(track *metadatapb.Track, prodInfo *ProductInfo, position int) *ApiResponseStatusTrack {
	var artists []string
	for _, a := range track.Artist {
		artists = append(artists, *a.Name)
	}

	var albumCoverId string
	if len(track.Album.Cover) > 0 {
		albumCoverId = hex.EncodeToString(track.Album.Cover[0].FileId)
	} else if track.Album.CoverGroup != nil && len(track.Album.CoverGroup.Image) > 0 {
		albumCoverId = hex.EncodeToString(track.Album.CoverGroup.Image[0].FileId)
	}

	return &ApiResponseStatusTrack{
		Uri:           librespot.TrackId(track.Gid).Uri(),
		Name:          *track.Name,
		ArtistNames:   artists,
		AlbumName:     *track.Album.Name,
		AlbumCoverUrl: prodInfo.ImageUrl(albumCoverId),
		Position:      position,
		Duration:      int(*track.Duration),
	}
}

type ApiResponseStatus struct {
	Username       string                  `json:"username"`
	DeviceId       string                  `json:"device_id"`
	DeviceType     string                  `json:"device_type"`
	DeviceName     string                  `json:"device_name"`
	PlayOrigin     string                  `json:"play_origin"`
	Stopped        bool                    `json:"stopped"`
	Paused         bool                    `json:"paused"`
	Buffering      bool                    `json:"buffering"`
	Volume         uint32                  `json:"volume"`
	VolumeSteps    uint32                  `json:"volume_steps"`
	RepeatContext  bool                    `json:"repeat_context"`
	RepeatTrack    bool                    `json:"repeat_track"`
	ShuffleContext bool                    `json:"shuffle_context"`
	Track          *ApiResponseStatusTrack `json:"track"`
}

type ApiResponseVolume struct {
	Value uint32 `json:"value"`
	Max   uint32 `json:"max"`
}

type ApiEvent struct {
	Type ApiEventType `json:"type"`
	Data any          `json:"data"`
}

type ApiEventDataTrack ApiResponseStatusTrack

type ApiEventDataVolume ApiResponseVolume

type ApiEventDataPlaying struct {
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataNotPlaying struct {
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataPaused struct {
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataStopped struct {
	PlayOrigin string `json:"play_origin"`
}

type ApiEventDataSeek struct {
	Position   int    `json:"position"`
	Duration   int    `json:"duration"`
	PlayOrigin string `json:"play_origin"`
}

func NewApiServer(port int) (_ *ApiServer, err error) {
	s := &ApiServer{}
	s.requests = make(chan ApiRequest)

	s.listener, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("failed starting api listener: %w", err)
	}

	go s.serve()
	return s, nil
}

func NewStubApiServer() (*ApiServer, error) {
	s := &ApiServer{}
	s.requests = make(chan ApiRequest)
	return s, nil
}

func (s *ApiServer) handleRequest(req ApiRequest, w http.ResponseWriter) {
	req.resp = make(chan apiResponse, 1)
	s.requests <- req
	resp := <-req.resp
	if errors.Is(resp.err, ErrNoSession) {
		w.WriteHeader(http.StatusNoContent)
		return
	} else if resp.err != nil {
		log.WithError(resp.err).Error("failed handling status request")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp.data)
}

func (s *ApiServer) serve() {
	m := http.NewServeMux()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	})
	m.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypeStatus}, w)
	})
	m.HandleFunc("/player/play", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var data ApiRequestDataPlay
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypePlay, Data: data}, w)
	})
	m.HandleFunc("/player/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypeResume}, w)
	})
	m.HandleFunc("/player/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypePause}, w)
	})
	m.HandleFunc("/player/next", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypeNext}, w)
	})
	m.HandleFunc("/player/prev", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypePrev}, w)
	})
	m.HandleFunc("/player/seek", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var data struct {
			Position int64 `json:"position"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypeSeek, Data: data.Position}, w)
	})
	m.HandleFunc("/player/volume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			s.handleRequest(ApiRequest{Type: ApiRequestTypeGetVolume}, w)
		} else if r.Method == "POST" {
			var data struct {
				Volume uint32 `json:"volume"`
			}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			s.handleRequest(ApiRequest{Type: ApiRequestTypeSetVolume, Data: data.Volume}, w)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	m.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			log.WithError(err).Error("failed accepting websocket connection")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// add the client to the list
		s.clientsLock.Lock()
		s.clients = append(s.clients, c)
		s.clientsLock.Unlock()

		log.Debugf("new websocket client")

		for {
			_, _, err := c.Read(context.Background())
			if s.close {
				return
			} else if err != nil {
				log.WithError(err).Error("websocket connection errored")

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
	})

	err := http.Serve(s.listener, m)
	if s.close {
		return
	} else if err != nil {
		log.WithError(err).Fatal("failed serving api")
	}
}

func (s *ApiServer) Emit(ev *ApiEvent) {
	s.clientsLock.RLock()
	defer s.clientsLock.RUnlock()

	log.Tracef("emitting websocket event: %s", ev.Type)

	for _, client := range s.clients {
		if err := wsjson.Write(context.TODO(), client, ev); err != nil {
			// purposely do not propagate this to the caller
			log.WithError(err).Error("failed communicating with websocket client")
		}
	}
}

func (s *ApiServer) Receive() <-chan ApiRequest {
	return s.requests
}

func (s *ApiServer) Close() {
	s.close = true

	// close all websocket clients
	s.clientsLock.RLock()
	for _, client := range s.clients {
		_ = client.Close(websocket.StatusGoingAway, "")
	}
	s.clientsLock.RUnlock()

	// close the listener
	_ = s.listener.Close()
}
