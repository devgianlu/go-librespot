package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
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
	ApiRequestTypeStatus ApiRequestType = "status"
	ApiRequestTypeResume ApiRequestType = "resume"
	ApiRequestTypePause  ApiRequestType = "pause"
	ApiRequestTypeSeek   ApiRequestType = "seek"
	ApiRequestTypePrev   ApiRequestType = "prev"
	ApiRequestTypeNext   ApiRequestType = "next"
)

type ApiRequest struct {
	Type ApiRequestType
	Data any

	resp chan apiResponse
}

func (r *ApiRequest) Reply(data any, err error) {
	r.resp <- apiResponse{data, err}
}

type apiResponse struct {
	data any
	err  error
}

type ApiResponseStatus struct {
	Username  string `json:"username"`
	TrackUri  string `json:"track_uri"`
	TrackName string `json:"track_name"`
}

type ApiEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type ApiEventDataTrack struct {
	Uri      string `json:"uri"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	Duration int    `json:"duration"`
}

type ApiEventDataVolume struct {
	Current int `json:"current"`
	Max     int `json:"max"`
}

type ApiEventDataSeek struct {
	Position int `json:"position"`
	Duration int `json:"duration"`
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
	m.HandleFunc("/player/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypeResume}, w)
	})
	m.HandleFunc("/status/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypePause}, w)
	})
	m.HandleFunc("/status/next", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypeNext}, w)
	})
	m.HandleFunc("/status/prev", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		s.handleRequest(ApiRequest{Type: ApiRequestTypePrev}, w)
	})
	m.HandleFunc("/status/seek", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var data struct {
			Position int64 `json:"position"`
		}
		_ = json.NewDecoder(r.Body).Decode(&data)
		s.handleRequest(ApiRequest{Type: ApiRequestTypeSeek, Data: data.Position}, w)
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
