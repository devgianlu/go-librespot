package spclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		return fmt.Errorf("invalid status code from collection write: %d", resp.StatusCode)
	}

	return nil
}

// PlaylistRevision returns the current revision of a playlist, which a change
// has to be based on.
func (c *Spclient) PlaylistRevision(ctx context.Context, playlist librespot.SpotifyId) ([]byte, error) {
	query := url.Values{}
	query.Set("decorate", "revision")
	query.Set("from", "0")
	query.Set("length", "1")

	resp, err := c.Request(ctx, "GET", fmt.Sprintf("/playlist/v2/playlist/%s", playlist.Base62()), query, nil, nil)
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

	var content playlist4pb.SelectedListContent
	if err := proto.Unmarshal(respBytes, &content); err != nil {
		return nil, fmt.Errorf("failed unmarshalling SelectedListContent: %w", err)
	}

	return content.GetRevision(), nil
}

// PlaylistAppend appends uris to the end of a playlist, as a change based on
// revision. It returns ErrPlaylistConflict when the playlist moved on since.
func (c *Spclient) PlaylistAppend(ctx context.Context, playlist librespot.SpotifyId, username string, revision []byte, uris []string) error {
	items := make([]*playlist4pb.Item, 0, len(uris))
	for _, uri := range uris {
		items = append(items, &playlist4pb.Item{Uri: proto.String(uri)})
	}

	body, err := proto.Marshal(&playlist4pb.ListChanges{
		BaseRevision: revision,
		Deltas: []*playlist4pb.Delta{{
			Ops: []*playlist4pb.Op{{
				Kind: playlist4pb.Op_ADD.Enum(),
				Add:  &playlist4pb.Add{Items: items, AddLast: proto.Bool(true)},
			}},
			Info: &playlist4pb.ChangeInfo{
				User:      proto.String(username),
				Timestamp: proto.Int64(time.Now().UnixMilli()),
			},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed marshalling ListChanges: %w", err)
	}

	resp, err := c.Request(ctx, "POST", fmt.Sprintf("/playlist/v2/playlist/%s/changes", playlist.Base62()), nil, nil, body)
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
		return fmt.Errorf("invalid status code from playlist changes: %d", resp.StatusCode)
	}
}
