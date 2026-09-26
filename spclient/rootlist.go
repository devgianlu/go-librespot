package spclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"google.golang.org/protobuf/proto"
)

// rootlistDecorations asks the playlist service to inline each entry's
// attributes (name, description, picture), length and owner into the
// meta_items of the response, so listing the rootlist takes one request
// rather than one per playlist.
const rootlistDecorations = "revision,attributes,length,owner,capabilities"

// Rootlist fetches a page of the user's rootlist: the playlists and folders
// shown in the library sidebar, in the user's order. Folders appear as
// spotify:start-group and spotify:end-group entries around their contents.
func (c *Spclient) Rootlist(ctx context.Context, username string, from, length int) (*playlist4pb.SelectedListContent, error) {
	query := url.Values{}
	query.Set("decorate", rootlistDecorations)
	query.Set("from", strconv.Itoa(from))
	query.Set("length", strconv.Itoa(length))

	resp, err := c.Request(ctx, "GET", fmt.Sprintf("/playlist/v2/user/%s/rootlist", url.PathEscape(username)), query, nil, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid status code from rootlist: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var protoResp playlist4pb.SelectedListContent
	if err := proto.Unmarshal(respBytes, &protoResp); err != nil {
		return nil, fmt.Errorf("failed unmarshalling SelectedListContent: %w", err)
	}

	return &protoResp, nil
}
