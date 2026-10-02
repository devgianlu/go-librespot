//go:build test_unit

package spclient_test

import (
	"net/http"

	librespot "github.com/devgianlu/go-librespot"
	collectionpb "github.com/devgianlu/go-librespot/proto/spotify/collection/v2"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"github.com/devgianlu/go-librespot/spclient"
	"google.golang.org/protobuf/proto"
)

func (suite *RequestSuite) TestCollectionWriteSavesToLikedSongs() {
	err := suite.spclient.CollectionWrite(suite.T().Context(), "user", spclient.CollectionSetLikedSongs,
		[]string{"spotify:track:a", "spotify:track:b"}, false)
	suite.Require().NoError(err)

	got := suite.requests()
	suite.Require().Len(got, 1)
	suite.Equal("POST", got[0].method)
	suite.Equal("/collection/v2/write", got[0].path)
	suite.Equal("application/vnd.collection-v2.spotify.proto", got[0].header.Get("Content-Type"))

	var req collectionpb.WriteRequest
	suite.Require().NoError(proto.Unmarshal(got[0].body, &req))
	suite.Equal("user", req.GetUsername())
	suite.Equal("collection", req.GetSet())
	suite.Require().Len(req.GetItems(), 2)
	suite.Equal("spotify:track:a", req.GetItems()[0].GetUri())
	suite.False(req.GetItems()[0].GetIsRemoved())
	suite.Positive(req.GetItems()[0].GetAddedAt())
}

func (suite *RequestSuite) TestCollectionWriteRemoves() {
	err := suite.spclient.CollectionWrite(suite.T().Context(), "user", spclient.CollectionSetLikedSongs,
		[]string{"spotify:track:a"}, true)
	suite.Require().NoError(err)

	var req collectionpb.WriteRequest
	suite.Require().NoError(proto.Unmarshal(suite.requests()[0].body, &req))
	suite.True(req.GetItems()[0].GetIsRemoved())
	suite.Zero(req.GetItems()[0].GetAddedAt())
}

func (suite *RequestSuite) TestCollectionWriteFailsOnError() {
	suite.handler = func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }

	err := suite.spclient.CollectionWrite(suite.T().Context(), "user", spclient.CollectionSetLikedSongs,
		[]string{"spotify:track:a"}, false)
	suite.Error(err)
}

func testPlaylistId(suite *RequestSuite) librespot.SpotifyId {
	id, err := librespot.SpotifyIdFromUri("spotify:playlist:37i9dQZF1DXcBWIGoYBM5M")
	suite.Require().NoError(err)
	return *id
}

func (suite *RequestSuite) TestPlaylistRevisionReadsRevision() {
	suite.handler = func(_ int, w http.ResponseWriter) {
		body, _ := proto.Marshal(&playlist4pb.SelectedListContent{Revision: []byte{1, 2, 3}})
		_, _ = w.Write(body)
	}

	revision, err := suite.spclient.PlaylistRevision(suite.T().Context(), testPlaylistId(suite))
	suite.Require().NoError(err)
	suite.Equal([]byte{1, 2, 3}, revision)

	got := suite.requests()[0]
	suite.Equal("/playlist/v2/playlist/37i9dQZF1DXcBWIGoYBM5M", got.path)
	suite.Equal("revision", got.query.Get("decorate"))
}

func (suite *RequestSuite) TestPlaylistAppendSendsAddLastChange() {
	err := suite.spclient.PlaylistAppend(suite.T().Context(), testPlaylistId(suite), "user", []byte{9},
		[]string{"spotify:track:a"})
	suite.Require().NoError(err)

	got := suite.requests()[0]
	suite.Equal("POST", got.method)
	suite.Equal("/playlist/v2/playlist/37i9dQZF1DXcBWIGoYBM5M/changes", got.path)
	suite.Equal("application/x-protobuf", got.header.Get("Content-Type"))

	var changes playlist4pb.ListChanges
	suite.Require().NoError(proto.Unmarshal(got.body, &changes))
	suite.Equal([]byte{9}, changes.GetBaseRevision())
	suite.Require().Len(changes.GetDeltas(), 1)
	delta := changes.GetDeltas()[0]
	suite.Equal("user", delta.GetInfo().GetUser())
	suite.Require().Len(delta.GetOps(), 1)
	op := delta.GetOps()[0]
	suite.Equal(playlist4pb.Op_ADD, op.GetKind())
	suite.True(op.GetAdd().GetAddLast())
	suite.Require().Len(op.GetAdd().GetItems(), 1)
	suite.Equal("spotify:track:a", op.GetAdd().GetItems()[0].GetUri())
}

func (suite *RequestSuite) TestPlaylistAppendReportsConflict() {
	suite.handler = func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusConflict) }

	err := suite.spclient.PlaylistAppend(suite.T().Context(), testPlaylistId(suite), "user", []byte{9},
		[]string{"spotify:track:a"})
	suite.ErrorIs(err, spclient.ErrPlaylistConflict)
}

func (suite *RequestSuite) TestCollectionContainsAsksForTheItems() {
	suite.handler = func(_ int, w http.ResponseWriter) {
		body, _ := proto.Marshal(&collectionpb.ContainsResponse{Found: []bool{true, false}})
		_, _ = w.Write(body)
	}

	found, err := suite.spclient.CollectionContains(suite.T().Context(), "user", spclient.CollectionSetLikedSongs,
		[]string{"spotify:track:a", "spotify:track:b"})
	suite.Require().NoError(err)
	suite.Equal([]bool{true, false}, found)

	got := suite.requests()[0]
	suite.Equal("/collection/v2/contains", got.path)
	suite.Equal("application/vnd.collection-v2.spotify.proto", got.header.Get("Content-Type"))

	var req collectionpb.ContainsRequest
	suite.Require().NoError(proto.Unmarshal(got.body, &req))
	suite.Equal("user", req.GetUsername())
	suite.Equal("collection", req.GetSet())
	suite.Require().Len(req.GetItems(), 2)
	suite.Equal("spotify:track:b", req.GetItems()[1].GetUri(), "items are collection items, not bare strings")
}

func (suite *RequestSuite) TestCollectionContainsRejectsAShortAnswer() {
	suite.handler = func(_ int, w http.ResponseWriter) {
		body, _ := proto.Marshal(&collectionpb.ContainsResponse{Found: []bool{true}})
		_, _ = w.Write(body)
	}

	_, err := suite.spclient.CollectionContains(suite.T().Context(), "user", spclient.CollectionSetLikedSongs,
		[]string{"spotify:track:a", "spotify:track:b"})
	suite.Error(err)
}

// An append the server may have applied before failing must not be resent,
// or the tracks would land twice.
func (suite *RequestSuite) TestPlaylistAppendIsSentOnce() {
	suite.handler = func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusGatewayTimeout) }

	err := suite.spclient.PlaylistAppend(suite.T().Context(), testPlaylistId(suite), "user", []byte{9}, []string{"spotify:track:a"})
	suite.Error(err)
	suite.Len(suite.requests(), 1)
}

func playlistPage(revision []byte, pos int32, truncated bool, uris ...string) []byte {
	items := make([]*playlist4pb.Item, len(uris))
	for i, uri := range uris {
		items[i] = &playlist4pb.Item{Uri: proto.String(uri)}
	}
	body, _ := proto.Marshal(&playlist4pb.SelectedListContent{
		Revision: revision,
		Contents: &playlist4pb.ListItems{Pos: proto.Int32(pos), Truncated: proto.Bool(truncated), Items: items},
	})
	return body
}

func (suite *RequestSuite) TestPlaylistContentsReadsAllPages() {
	suite.handler = func(attempt int, w http.ResponseWriter) {
		if attempt == 0 {
			_, _ = w.Write(playlistPage([]byte{1}, 0, true, "spotify:track:a", "spotify:track:b"))
			return
		}
		_, _ = w.Write(playlistPage([]byte{1}, 2, false, "spotify:track:c"))
	}

	revision, uris, err := suite.spclient.PlaylistContents(suite.T().Context(), testPlaylistId(suite))
	suite.Require().NoError(err)
	suite.Equal([]byte{1}, revision)
	suite.Equal([]string{"spotify:track:a", "spotify:track:b", "spotify:track:c"}, uris)

	got := suite.requests()
	suite.Require().Len(got, 2)
	suite.Equal("0", got[0].query.Get("from"))
	suite.Equal("2", got[1].query.Get("from"), "the next page starts after the items read")
}

func (suite *RequestSuite) TestPlaylistContentsReportsAChangeBetweenPages() {
	suite.handler = func(attempt int, w http.ResponseWriter) {
		_, _ = w.Write(playlistPage([]byte{byte(attempt)}, int32(attempt), attempt == 0, "spotify:track:a"))
	}

	_, _, err := suite.spclient.PlaylistContents(suite.T().Context(), testPlaylistId(suite))
	suite.ErrorIs(err, spclient.ErrPlaylistConflict)
}

func (suite *RequestSuite) TestPlaylistRemoveSendsAnIndexRange() {
	err := suite.spclient.PlaylistRemove(suite.T().Context(), testPlaylistId(suite), "user", []byte{9}, "spotify:track:b", 4)
	suite.Require().NoError(err)

	var changes playlist4pb.ListChanges
	suite.Require().NoError(proto.Unmarshal(suite.requests()[0].body, &changes))
	suite.Equal([]byte{9}, changes.GetBaseRevision())
	op := changes.GetDeltas()[0].GetOps()[0]
	suite.Equal(playlist4pb.Op_REM, op.GetKind())
	suite.Equal(int32(4), op.GetRem().GetFromIndex())
	suite.Equal(int32(1), op.GetRem().GetLength())
	suite.False(op.GetRem().GetItemsAsKey(), "keyed by the item, Spotify would remove every duplicate of it")
	suite.Equal("spotify:track:b", op.GetRem().GetItems()[0].GetUri())
}
