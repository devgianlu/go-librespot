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

func NewSpclient(addr, clientToken string) (*Spclient, error) {
	baseUrl, err := url.Parse(fmt.Sprintf("https://%s/", addr))
	if err != nil {
		return nil, fmt.Errorf("invalid spclient base url: %w", err)
	}

	return &Spclient{baseUrl: baseUrl, client: &http.Client{}, clientToken: clientToken}, nil
}
