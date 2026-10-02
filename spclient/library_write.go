package spclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	collectionpb "github.com/devgianlu/go-librespot/proto/spotify/collection/v2"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"google.golang.org/protobuf/proto"
)

// collectionContentType is the media type the collection service speaks.
const collectionContentType = "application/vnd.collection-v2.spotify.proto"

// CollectionSetLikedSongs is the collection set holding the user's Liked Songs.
const CollectionSetLikedSongs = "collection"

// StatusError reports a status the collection or playlist service answered
// a library request with, so callers can tell a permission problem (403) or a
// missing playlist (404) from a fault.
type StatusError struct {
	Op         string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("invalid status code from %s: %d", e.Op, e.StatusCode)
}

// ErrPlaylistConflict reports that a playlist changed between reading its
// revision and writing to it.
var ErrPlaylistConflict = errors.New("playlist changed concurrently")

// CollectionWrite adds uris to, or with remove set removes them from, one of
// the user's collection sets.
func (c *Spclient) CollectionWrite(ctx context.Context, username, set string, uris []string, remove bool) error {
	now := time.Now().Unix()
	req := &collectionpb.WriteRequest{Username: username, Set: set}
	for _, uri := range uris {
		item := &collectionpb.CollectionItem{Uri: uri, IsRemoved: remove}
		if !remove {
			item.AddedAt = now
		}
		req.Items = append(req.Items, item)
	}

	body, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed marshalling WriteRequest: %w", err)
	}

	resp, err := c.Request(ctx, "POST", "/collection/v2/write", nil, http.Header{
		"Content-Type": {collectionContentType},
		"Accept":       {collectionContentType},
	}, body)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return &StatusError{Op: "collection write", StatusCode: resp.StatusCode}
	}

	return nil
}

// CollectionContains tells for each of uris whether it is in one of the
// user's collection sets, in one round trip.
func (c *Spclient) CollectionContains(ctx context.Context, username, set string, uris []string) ([]bool, error) {
	req := &collectionpb.ContainsRequest{Username: username, Set: set}
	for _, uri := range uris {
		req.Items = append(req.Items, &collectionpb.CollectionItem{Uri: uri})
	}

	body, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling ContainsRequest: %w", err)
	}

	resp, err := c.Request(ctx, "POST", "/collection/v2/contains", nil, http.Header{
		"Content-Type": {collectionContentType},
		"Accept":       {collectionContentType},
	}, body)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "collection contains", StatusCode: resp.StatusCode}
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var contains collectionpb.ContainsResponse
	if err := proto.Unmarshal(respBytes, &contains); err != nil {
		return nil, fmt.Errorf("failed unmarshalling ContainsResponse: %w", err)
	}
	if len(contains.GetFound()) != len(uris) {
		return nil, fmt.Errorf("collection contains answered %d items for %d", len(contains.GetFound()), len(uris))
	}

	return contains.GetFound(), nil
}

// PlaylistRevision returns the current revision of a playlist, which a change
// has to be based on.
func (c *Spclient) PlaylistRevision(ctx context.Context, playlist librespot.SpotifyId) ([]byte, error) {
	query := url.Values{}
	query.Set("decorate", "revision")
	query.Set("from", "0")
	query.Set("length", "1")

	content, err := c.getPlaylist(ctx, playlist, query)
	if err != nil {
		return nil, err
	}
	return content.GetRevision(), nil
}

// playlistContentsPageSize is how many items PlaylistContents reads per page.
const playlistContentsPageSize = 1000

// playlistMaxPages caps the pages read for one playlist.
const playlistMaxPages = 50

// PlaylistContents returns a playlist's revision and its item URIs in order,
// so items can be found by position. It reports ErrPlaylistConflict when the
// playlist changes between two pages.
func (c *Spclient) PlaylistContents(ctx context.Context, playlist librespot.SpotifyId) ([]byte, []string, error) {
	var revision []byte
	var uris []string
	for page := 0; page < playlistMaxPages; page++ {
		query := url.Values{}
		query.Set("decorate", "revision")
		query.Set("from", strconv.Itoa(len(uris)))
		query.Set("length", strconv.Itoa(playlistContentsPageSize))

		content, err := c.getPlaylist(ctx, playlist, query)
		if err != nil {
			return nil, nil, err
		}
		if page == 0 {
			revision = content.GetRevision()
		} else if !bytes.Equal(revision, content.GetRevision()) {
			return nil, nil, ErrPlaylistConflict
		}

		items := content.GetContents().GetItems()
		for _, item := range items {
			uris = append(uris, item.GetUri())
		}
		if !content.GetContents().GetTruncated() || len(items) == 0 {
			return revision, uris, nil
		}
	}
	return nil, nil, fmt.Errorf("playlist still truncated after %d pages", playlistMaxPages)
}

func (c *Spclient) getPlaylist(ctx context.Context, playlist librespot.SpotifyId, query url.Values) (*playlist4pb.SelectedListContent, error) {
	resp, err := c.Request(ctx, "GET", fmt.Sprintf("/playlist/v2/playlist/%s", playlist.Base62()), query, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "playlist", StatusCode: resp.StatusCode}
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var content playlist4pb.SelectedListContent
	if err := proto.Unmarshal(respBytes, &content); err != nil {
		return nil, fmt.Errorf("failed unmarshalling SelectedListContent: %w", err)
	}
	return &content, nil
}

// PlaylistRemove removes the one item at position, which must be uri, as a
// change based on revision. It returns ErrPlaylistConflict when the playlist
// moved on since.
//
// The removal is by index range. Rem.items_as_key would make the items the
// key instead and remove every entry of uri, duplicates included.
func (c *Spclient) PlaylistRemove(ctx context.Context, playlist librespot.SpotifyId, username string, revision []byte, uri string, position int) error {
	return c.playlistChange(ctx, playlist, username, revision, &playlist4pb.Op{
		Kind: playlist4pb.Op_REM.Enum(),
		Rem: &playlist4pb.Rem{
			FromIndex: proto.Int32(int32(position)),
			Length:    proto.Int32(1),
			Items:     []*playlist4pb.Item{{Uri: proto.String(uri)}},
		},
	})
}

// PlaylistAppend appends uris to the end of a playlist, as a change based on
// revision. It returns ErrPlaylistConflict when the playlist moved on since.
func (c *Spclient) PlaylistAppend(ctx context.Context, playlist librespot.SpotifyId, username string, revision []byte, uris []string) error {
	items := make([]*playlist4pb.Item, 0, len(uris))
	for _, uri := range uris {
		items = append(items, &playlist4pb.Item{Uri: proto.String(uri)})
	}

	return c.playlistChange(ctx, playlist, username, revision, &playlist4pb.Op{
		Kind: playlist4pb.Op_ADD.Enum(),
		Add:  &playlist4pb.Add{Items: items, AddLast: proto.Bool(true)},
	})
}

// playlistChange sends one operation as a change to a playlist based on
// revision.
func (c *Spclient) playlistChange(ctx context.Context, playlist librespot.SpotifyId, username string, revision []byte, op *playlist4pb.Op) error {
	body, err := proto.Marshal(&playlist4pb.ListChanges{
		BaseRevision: revision,
		Deltas: []*playlist4pb.Delta{{
			Ops: []*playlist4pb.Op{op},
			Info: &playlist4pb.ChangeInfo{
				User:      proto.String(username),
				Timestamp: proto.Int64(time.Now().UnixMilli()),
			},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed marshalling ListChanges: %w", err)
	}

	// A change is not idempotent: resent after the server applied it but its
	// answer got lost, an append would land twice.
	resp, err := c.RequestOnce(ctx, "POST", fmt.Sprintf("/playlist/v2/playlist/%s/changes", playlist.Base62()), nil, nil, body)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrPlaylistConflict
	default:
		return &StatusError{Op: "playlist changes", StatusCode: resp.StatusCode}
	}
}
