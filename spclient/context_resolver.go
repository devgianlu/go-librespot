package spclient

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	connectpb "go-librespot/proto/spotify/connectstate"
	"io"
	"strings"
)

type ContextResolver struct {
	sp *Spclient

	typ librespot.SpotifyIdType
	ctx *connectpb.Context
}

func NewContextResolver(sp *Spclient, ctx *connectpb.Context) (*ContextResolver, error) {
	typ := librespot.InferSpotifyIdTypeFromContextUri(ctx.Uri)
	if len(ctx.Pages) > 0 {
		return &ContextResolver{sp, typ, ctx}, nil
	} else {
		newCtx, err := sp.ContextResolve(ctx.Uri)
		if err != nil {
			return nil, fmt.Errorf("failed resolving context %s: %w", ctx.Uri, err)
		} else if newCtx.Loading {
			return nil, fmt.Errorf("context %s is loading", newCtx.Uri)
		}

		if newCtx.Metadata == nil {
			newCtx.Metadata = map[string]string{}
		}
		for key, val := range ctx.Metadata {
			newCtx.Metadata[key] = val
		}

		return &ContextResolver{sp, typ, newCtx}, nil
	}
}

func (r *ContextResolver) Type() librespot.SpotifyIdType {
	return r.typ
}

func (r *ContextResolver) Uri() string {
	return r.ctx.Uri
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
	resp, err := r.sp.request("GET", url, nil, nil, nil)
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

	log.Tracef("fetched new page from %s (has next: %t)", contextPage.PageUrl, len(contextPage.NextPageUrl) > 0)

	return &contextPage, nil
}

func (r *ContextResolver) Page(idx int) ([]*connectpb.ContextTrack, error) {
	for idx >= len(r.ctx.Pages) {
		lastPage := r.ctx.Pages[len(r.ctx.Pages)-1]
		if len(lastPage.NextPageUrl) == 0 {
			return nil, io.EOF
		}

		newPage, err := r.loadPage(lastPage.NextPageUrl)
		if err != nil {
			return nil, fmt.Errorf("failed fetching next page: %w", err)
		}

		r.ctx.Pages = append(r.ctx.Pages, newPage)
	}

	page := r.ctx.Pages[idx]
	if page.Loading {
		return nil, fmt.Errorf("context page is loading")
	}

	if len(page.Tracks) == 0 {
		if len(page.PageUrl) == 0 {
			return nil, fmt.Errorf("invalid empty page without url")
		}

		newPage, err := r.loadPage(page.PageUrl)
		if err != nil {
			return nil, fmt.Errorf("failed fetching page: %w", err)
		}

		r.ctx.Pages[idx] = newPage
		page = newPage
	}

	if len(page.Tracks) == 0 {
		log.Warnf("returning empty context page (%s) for %s", page.PageUrl, r.ctx.Uri)
	}

	return page.Tracks, nil
}
