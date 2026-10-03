package spclient

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"google.golang.org/protobuf/proto"
)

// playlistPageSize is how many items a page of a playlist context holds. The
// playlist service answers a whole playlist at once when asked, which for one
// of thousands of items takes the better part of a second, for a first track
// that usually sits near the top. The rest is fetched page by page, as playback
// gets there.
const playlistPageSize = 50

const playlistPageUrlPrefix = "hm://playlist/v2/playlist/"

func playlistPageUrl(playlist librespot.SpotifyId, from int) string {
	return fmt.Sprintf("%s%s?from=%d&length=%d", playlistPageUrlPrefix, playlist.Base62(), from, playlistPageSize)
}

// IsPlaylistPageUrl reports whether a context page url names a page of a
// playlist context, to be loaded with PlaylistPage.
func IsPlaylistPageUrl(url string) bool {
	return strings.HasPrefix(url, playlistPageUrlPrefix)
}

// PlaylistContext builds the context of a playlist from the playlist service,
// as the official client does, rather than through context resolve. Context
// resolve leaves out the podcast episodes of a playlist that mixes them with
// tracks; the playlist service lists every item, and the rest of what context
// resolve answers is derived from it unchanged: the same uids, and the same
// context and track metadata.
//
// Only the first page is fetched here. The pages after it are named by
// NextPageUrl, and loaded with PlaylistPage.
func (c *Spclient) PlaylistContext(ctx context.Context, playlist librespot.SpotifyId) (*connectpb.Context, error) {
	list, err := c.playlist(ctx, playlistPageUrl(playlist, 0))
	if err != nil {
		return nil, err
	}

	return &connectpb.Context{
		Uri:      playlist.Uri(),
		Url:      "context://" + playlist.Uri(),
		Metadata: playlistMetadata(list),
		Pages:    []*connectpb.ContextPage{playlistContextPage(playlist, list)},
	}, nil
}

// PlaylistPage loads the page of a playlist context a page url names.
func (c *Spclient) PlaylistPage(ctx context.Context, pageUrl string) (*connectpb.ContextPage, error) {
	base62, _, _ := strings.Cut(strings.TrimPrefix(pageUrl, playlistPageUrlPrefix), "?")
	playlist, err := librespot.SpotifyIdFromBase62(librespot.SpotifyIdTypePlaylist, base62)
	if err != nil {
		return nil, fmt.Errorf("invalid playlist page url %s: %w", pageUrl, err)
	}

	list, err := c.playlist(ctx, pageUrl)
	if err != nil {
		return nil, err
	}

	return playlistContextPage(*playlist, list), nil
}

func (c *Spclient) playlist(ctx context.Context, pageUrl string) (*playlist4pb.SelectedListContent, error) {
	resp, err := c.RequestHm(ctx, "GET", pageUrl, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid status code from playlist: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var list playlist4pb.SelectedListContent
	if err := proto.Unmarshal(respBytes, &list); err != nil {
		return nil, fmt.Errorf("failed unmarshalling SelectedListContent: %w", err)
	}

	return &list, nil
}

// playlistMetadata is the context metadata context resolve gives a playlist.
func playlistMetadata(list *playlist4pb.SelectedListContent) map[string]string {
	attrs := list.GetAttributes()

	metadata := map[string]string{
		"context_description": attrs.GetName(),
		"context_owner":       list.GetOwnerUsername(),
		"playlist.revision":   hex.EncodeToString(list.GetRevision()),
	}
	// Context resolve only gives a long description to a playlist that has a
	// description, and then it is the name all the same.
	if attrs.GetDescription() != "" {
		metadata["context_long_description"] = attrs.GetName()
	}
	if format := attrs.GetFormat(); format != "" {
		metadata["format_list_type"] = format
	}
	// A picture of its own. An editorial playlist names its image among its
	// format attributes instead, which come after and win.
	if picture := attrs.GetPicture(); len(picture) > 0 {
		metadata["image_url"] = "https://u.scdn.co/images/pl/default/" + hex.EncodeToString(picture)
	}
	for _, attr := range attrs.GetFormatAttributes() {
		metadata[attr.GetKey()] = attr.GetValue()
	}

	return metadata
}

// playlistContextPage maps a page of a playlist onto the tracks context resolve
// gives for it, with the items context resolve leaves out included, and names
// the page after it while there is one.
func playlistContextPage(playlist librespot.SpotifyId, list *playlist4pb.SelectedListContent) *connectpb.ContextPage {
	owner := list.GetOwnerUsername()
	items := list.GetContents().GetItems()

	tracks := make([]*connectpb.ContextTrack, 0, len(items))
	for _, item := range items {
		attrs := item.GetAttributes()
		uid := hex.EncodeToString(attrs.GetItemId())

		// An item nobody in particular added, as in editorial playlists, counts
		// as added by the owner.
		addedBy := attrs.GetAddedBy()
		if addedBy == "" {
			addedBy = owner
		}

		metadata := map[string]string{
			"added_by_username": addedBy,
			"highlight_id":      uid,
		}
		// Generated playlists, like the daily mixes, give their items no time.
		if attrs.Timestamp != nil {
			metadata["added_at"] = strconv.FormatInt(attrs.GetTimestamp(), 10)
		}
		for _, attr := range attrs.GetFormatAttributes() {
			metadata[attr.GetKey()] = attr.GetValue()
		}

		tracks = append(tracks, &connectpb.ContextTrack{
			Uri:      item.GetUri(),
			Uid:      uid,
			Metadata: metadata,
		})
	}

	page := &connectpb.ContextPage{Tracks: tracks}
	if list.GetContents().GetTruncated() && len(items) > 0 {
		page.NextPageUrl = playlistPageUrl(playlist, int(list.GetContents().GetPos())+len(items))
	}
	return page
}
