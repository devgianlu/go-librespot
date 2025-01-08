package spclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"golang.org/x/exp/maps"
)

type ContextResolver struct {
	log librespot.Logger

	sp *Spclient

	typ librespot.SpotifyIdType
	ctx *connectpb.Context
}

func isTracksComplete(ctx *connectpb.Context) bool {
	expectedNumberOfTracks := -1
	var err error = nil
	for _, key := range maps.Keys(ctx.Metadata) {
		if key == "playlist_number_of_tracks" {
			expectedNumberOfTracks, err = strconv.Atoi(ctx.Metadata[key])
			break
		}
	}

	// this method should not result in errors, it's just a "guess"
	if err != nil || expectedNumberOfTracks < 0 {
		return true
	}

	totalLength := 0
	for _, page := range ctx.Pages {
		totalLength += len(page.Tracks)
		if len(page.NextPageUrl) != 0 {
			return true
		}
	}

	return expectedNumberOfTracks == totalLength
}

func NewContextResolver(ctx context.Context, log librespot.Logger, sp *Spclient, spotCtx *connectpb.Context) (_ *ContextResolver, err error) {
	typ := librespot.InferSpotifyIdTypeFromContextUri(spotCtx.Uri)

	if len(spotCtx.Pages) == 0 || !isTracksComplete(spotCtx) {
		newSpotCtx, err := sp.ContextResolve(ctx, spotCtx.Uri)
		if err != nil {
			return nil, fmt.Errorf("failed resolving context %s: %w", spotCtx.Uri, err)
		} else if newSpotCtx.Loading {
			return nil, fmt.Errorf("context %s is loading", newSpotCtx.Uri)
		}

		if newSpotCtx.Metadata == nil {
			newSpotCtx.Metadata = map[string]string{}
		}
		for key, val := range spotCtx.Metadata {
			newSpotCtx.Metadata[key] = val
		}

		spotCtx = newSpotCtx
	}

	autoplay := strings.HasPrefix(spotCtx.Uri, "spotify:station:")
	for _, page := range spotCtx.Pages {
		for _, track := range page.Tracks {
			if autoplay {
				track.Metadata["autoplay.is_autoplay"] = "true"
			}
		}
	}

	return &ContextResolver{log, sp, typ, spotCtx}, nil
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

func (r *ContextResolver) loadPage(ctx context.Context, url string) (*connectpb.ContextPage, error) {
	if !strings.HasPrefix(url, "hm://") {
		return nil, fmt.Errorf("invalid page url: %s", url)
	}

	url = url[5:]
	resp, err := r.sp.Request(ctx, "GET", url, nil, nil, nil)
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

	r.log.WithField("uri", r.Uri()).
		Tracef("fetched new page from %s (has next: %t)", contextPage.PageUrl, len(contextPage.NextPageUrl) > 0)

	return &contextPage, nil
}

func (r *ContextResolver) Page(ctx context.Context, idx int) ([]*connectpb.ContextTrack, error) {
	for idx >= len(r.ctx.Pages) {
		lastPage := r.ctx.Pages[len(r.ctx.Pages)-1]
		if len(lastPage.NextPageUrl) == 0 {

			return nil, io.EOF
		}

		newPage, err := r.loadPage(ctx, lastPage.NextPageUrl)
		if err != nil {
			return nil, fmt.Errorf("failed fetching next page: %w", err)
		}

		r.ctx.Pages = append(r.ctx.Pages, newPage)
	}

	page := r.ctx.Pages[idx]
	if len(page.Tracks) == 0 {
		if len(page.PageUrl) == 0 {
			return nil, fmt.Errorf("invalid empty page without url")
		}

		newPage, err := r.loadPage(ctx, page.PageUrl)
		if err != nil {
			return nil, fmt.Errorf("failed fetching page: %w", err)
		}

		r.ctx.Pages[idx] = newPage
		page = newPage
	}

	if len(page.Tracks) == 0 {
		r.log.Warnf("returning empty context page (%s) for %s", page.PageUrl, r.ctx.Uri)
	}

	return page.Tracks, nil
}
