package apresolve

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type apResolveResponse struct {
	Accesspoint []string `json:"accesspoint,omitempty"`
	Dealer      []string `json:"dealer,omitempty"`
	Spclient    []string `json:"spclient,omitempty"`
}

type ApResolver struct {
	baseUrl *url.URL

	endpoints     map[endpointType][]string
	endpointsExp  time.Time
	endpointsLock sync.RWMutex

	client http.Client
}

func NewApResolver() *ApResolver {
	baseUrl, err := url.Parse("https://apresolve.spotify.com/")
	if err != nil {
		panic("invalid apresolve base URL")
	}

	return &ApResolver{baseUrl: baseUrl, endpoints: map[endpointType][]string{}}
}

func (r *ApResolver) fetchUrls(types ...endpointType) error {
	if r.endpointsExp.After(time.Now()) {
		return nil
	}

	query := url.Values{}
	for _, type_ := range types {
		query.Add("type", string(type_))
	}

	reqUrl := *r.baseUrl
	reqUrl.RawQuery = query.Encode()

	// TODO: customize user agent
	resp, err := r.client.Do(&http.Request{
		Method: "GET",
		URL:    &reqUrl,
	})
	if err != nil {
		return fmt.Errorf("failed fetching apresolve URL: %w", err)
	}

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
		log.Debugf("fetched new accesspoints: %v", respJson.Accesspoint)
	}
	if slices.Contains(types, endpointTypeDealer) {
		r.endpoints[endpointTypeDealer] = respJson.Dealer
		log.Debugf("fetched new dealers: %v", respJson.Dealer)
	}
	if slices.Contains(types, endpointTypeSpclient) {
		r.endpoints[endpointTypeSpclient] = respJson.Spclient
		log.Debugf("fetched new spclients: %v", respJson.Spclient)
	}

	r.endpointsExp = time.Now().Add(1 * time.Hour)

	return nil
}

func (r *ApResolver) FetchAll() error {
	return r.fetchUrls(endpointTypeAccesspoint, endpointTypeDealer, endpointTypeSpclient)
}

func (r *ApResolver) get(type_ endpointType) (string, error) {
	if err := r.fetchUrls(type_); err != nil {
		return "", err
	}

	r.endpointsLock.RLock()
	defer r.endpointsLock.RUnlock()

	aps, ok := r.endpoints[type_]
	if !ok || len(aps) == 0 {
		return "", fmt.Errorf("no %s endpoint present", type_)
	}

	// TODO: perhaps we should get the first one, but choose another one if we have problems
	return aps[rand.Intn(len(aps))], nil
}

func (r *ApResolver) GetAccessPoint() (string, error) {
	return r.get(endpointTypeAccesspoint)
}

func (r *ApResolver) GetSpclient() (string, error) {
	return r.get(endpointTypeSpclient)
}

func (r *ApResolver) GetDealer() (string, error) {
	return r.get(endpointTypeDealer)
}
