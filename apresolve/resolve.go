package apresolve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
)

type apResolveResponse struct {
	Accesspoint []string `json:"accesspoint,omitempty"`
	Dealer      []string `json:"dealer,omitempty"`
	Spclient    []string `json:"spclient,omitempty"`
}

type ApResolver struct {
	baseUrl *url.URL

	endpoints     map[endpointType][]string
	endpointsExp  map[endpointType]time.Time
	endpointsLock sync.RWMutex

	client *http.Client
}

func NewApResolver(client *http.Client) *ApResolver {
	baseUrl, err := url.Parse("https://apresolve.spotify.com/")
	if err != nil {
		panic("invalid apresolve base URL")
	}

	return &ApResolver{
		baseUrl:      baseUrl,
		client:       client,
		endpoints:    map[endpointType][]string{},
		endpointsExp: map[endpointType]time.Time{},
	}
}

func (r *ApResolver) fetchUrls(ctx context.Context, types ...endpointType) error {
	anyExpired := false
	r.endpointsLock.RLock()
	for _, type_ := range types {
		if exp, ok := r.endpointsExp[type_]; !ok {
			anyExpired = true
			break
		} else if exp.Before(time.Now()) {
			anyExpired = true
			break
		}
	}
	r.endpointsLock.RUnlock()

	if !anyExpired {
		return nil
	}

	query := url.Values{}
	for _, type_ := range types {
		query.Add("type", string(type_))
	}

	reqUrl := *r.baseUrl
	reqUrl.RawQuery = query.Encode()

	req := &http.Request{
		Method: "GET",
		URL:    &reqUrl,
		Header: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
		},
	}

	resp, err := r.client.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed fetching apresolve URL: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return fmt.Errorf("invalid status code from apresolve: %d", resp.StatusCode)
	}

	var respJson apResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&respJson); err != nil {
		return fmt.Errorf("failed unmarhsalling apresolve response: %w", err)
	}

	r.endpointsLock.Lock()
	defer r.endpointsLock.Unlock()

	if slices.Contains(types, endpointTypeAccesspoint) {
		r.endpoints[endpointTypeAccesspoint] = respJson.Accesspoint
		r.endpointsExp[endpointTypeAccesspoint] = time.Now().Add(1 * time.Hour)
		log.Debugf("fetched new accesspoints: %v", respJson.Accesspoint)
	}
	if slices.Contains(types, endpointTypeDealer) {
		r.endpoints[endpointTypeDealer] = respJson.Dealer
		r.endpointsExp[endpointTypeDealer] = time.Now().Add(1 * time.Hour)
		log.Debugf("fetched new dealers: %v", respJson.Dealer)
	}
	if slices.Contains(types, endpointTypeSpclient) {
		r.endpoints[endpointTypeSpclient] = respJson.Spclient
		r.endpointsExp[endpointTypeSpclient] = time.Now().Add(1 * time.Hour)
		log.Debugf("fetched new spclients: %v", respJson.Spclient)
	}

	return nil
}

func (r *ApResolver) FetchAll(ctx context.Context) error {
	return r.fetchUrls(ctx, endpointTypeAccesspoint, endpointTypeDealer, endpointTypeSpclient)
}

func (r *ApResolver) get(ctx context.Context, type_ endpointType) ([]string, error) {
	if err := r.fetchUrls(ctx, type_); err != nil {
		return nil, err
	}

	r.endpointsLock.RLock()
	defer r.endpointsLock.RUnlock()

	aps, ok := r.endpoints[type_]
	if !ok || len(aps) == 0 {
		return nil, fmt.Errorf("no %s endpoint present", type_)
	}

	return aps, nil
}

func (r *ApResolver) getFunc(ctx context.Context, type_ endpointType) (librespot.GetAddressFunc, error) {
	addrs, err := r.get(ctx, type_)
	if err != nil {
		return nil, err
	}

	idx := 0
	return func(innerCtx context.Context) string {
		// if we haven't overflowed the available addresses, return one
		if idx < len(addrs) {
			newAddr := addrs[idx]
			idx++
			return newAddr
		}

		// try fetching new addresses
		newAddrs, err := r.get(innerCtx, type_)
		if err != nil {
			// if we cannot fetch new endpoints, eat it and return the first one
			log.WithError(err).Warnf("failed fetching new endpoint for %s", type_)
			return addrs[0]
		}

		// replace the old addresses, return the first one and set index for the next iteration
		addrs = newAddrs
		idx = 1
		return addrs[0]
	}, nil
}

func (r *ApResolver) GetAccesspoint(ctx context.Context) (librespot.GetAddressFunc, error) {
	return r.getFunc(ctx, endpointTypeAccesspoint)
}

func (r *ApResolver) GetSpclient(ctx context.Context) (librespot.GetAddressFunc, error) {
	return r.getFunc(ctx, endpointTypeSpclient)
}

func (r *ApResolver) GetDealer(ctx context.Context) (librespot.GetAddressFunc, error) {
	return r.getFunc(ctx, endpointTypeDealer)
}
