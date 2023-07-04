package spclient

import (
	"encoding/json"
	"errors"
	"fmt"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"io"
	"strings"
)

var ErrNoMorePages = errors.New("no more pages")

type ContextResolver struct {
	sp *Spclient

	ctx *connectpb.Context
}

func NewContextResolver(sp *Spclient, uri string) (*ContextResolver, error) {
	ctx, err := sp.ContextResolve(uri)
	if err != nil {
		return nil, fmt.Errorf("failed resolving context %s: %w", uri, err)
	} else if ctx.Loading {
		return nil, fmt.Errorf("context %s is loading", uri)
	}

	return &ContextResolver{sp, ctx}, nil
}

func (r *ContextResolver) Metadata() map[string]string {
	return r.ctx.Metadata
}

func (r *ContextResolver) Restrictions() *connectpb.Restrictions {
	return r.ctx.Restrictions
}

func (r *ContextResolver) loadPage(url string) (*connectpb.ContextPage, error) {
	if !strings.HasPrefix(url, "hm://") {
		return nil, fmt.Errorf("invalid page url: %s", url)
	}

	url = url[5:]
	resp, err := r.sp.request("GET", url, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed requesting page at %s: %w", url, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from page at %s: %d", url, resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading page response body: %w", err)
	}

	var contextPage connectpb.ContextPage
	if err := json.Unmarshal(respBytes, &contextPage); err != nil {
		return nil, fmt.Errorf("failed json unmarshalling ContextPage: %w", err)
	}

	return &contextPage, nil
}

func (r *ContextResolver) Page(idx int) ([]*connectpb.ContextTrack, map[string]string, error) {
	for idx >= len(r.ctx.Pages) {
		lastPage := r.ctx.Pages[len(r.ctx.Pages)-1]
		if len(lastPage.NextPageUrl) == 0 {
			return nil, nil, ErrNoMorePages
		}

		newPage, err := r.loadPage(lastPage.NextPageUrl)
		if err != nil {
			return nil, nil, fmt.Errorf("failed fetching next page: %w", err)
		}

		r.ctx.Pages = append(r.ctx.Pages, newPage)
	}

	page := r.ctx.Pages[idx]
	if page.Loading {
		return nil, nil, fmt.Errorf("context page is loading")
	}

	if len(page.Tracks) == 0 {
		if len(page.PageUrl) == 0 {
			return nil, nil, fmt.Errorf("invalid empty page without url")
		}

		newPage, err := r.loadPage(page.PageUrl)
		if err != nil {
			return nil, nil, fmt.Errorf("failed fetching page: %w", err)
		}

		// TODO: do we need to preserve any field?
		r.ctx.Pages[idx] = newPage
	}

	return page.Tracks, page.Metadata, nil
}
