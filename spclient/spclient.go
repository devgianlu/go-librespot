package spclient

import (
	"fmt"
	"net/http"
	"net/url"
)

type Spclient struct {
	baseUrl *url.URL

	client      *http.Client
	clientToken string
}

func NewSpclient(addr, deviceId string) (*Spclient, error) {
	baseUrl, err := url.Parse(fmt.Sprintf("https://%s/", addr))
	if err != nil {
		return nil, fmt.Errorf("invalid spclient base url: %w", err)
	}

	client := &http.Client{}

	// TODO: make client token persistent
	clientToken, err := retrieveClientToken(client, deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed retrieving client token: %w", err)
	}

	return &Spclient{baseUrl: baseUrl, client: client, clientToken: clientToken}, nil
}
