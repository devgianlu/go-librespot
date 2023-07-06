package main

import (
	"context"
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

type ApiRequest struct {
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

func (s *ApiServer) serve() {
	m := http.NewServeMux()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
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
