//go:build test_unit

package spclient_test

import (
	"io"
	"net/http"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/spclient"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"google.golang.org/protobuf/proto"
)

const testPlaylistUri = "spotify:playlist:4csZj4lePVm2yAf5Lg7ZXt"

func playlistItem(uri string, itemId []byte, addedBy string, formatAttrs ...*playlist4pb.FormatListAttribute) *playlist4pb.Item {
	attrs := &playlist4pb.ItemAttributes{
		ItemId:           itemId,
		Timestamp:        proto.Int64(1791031004968),
		FormatAttributes: formatAttrs,
	}
	if addedBy != "" {
		attrs.AddedBy = proto.String(addedBy)
	}
	return &playlist4pb.Item{Uri: proto.String(uri), Attributes: attrs}
}

func (suite *RequestSuite) answerWith(answers ...func(w http.ResponseWriter)) {
	suite.handler = func(attempt int, w http.ResponseWriter) {
		if attempt >= len(answers) {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		answers[attempt](w)
	}
}

func protoAnswer(suite *RequestSuite, msg proto.Message) func(w http.ResponseWriter) {
	body, err := proto.Marshal(msg)
	suite.Require().NoError(err)
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func contextTracks(spotCtx *connectpb.Context) []*connectpb.ContextTrack {
	var tracks []*connectpb.ContextTrack
	for _, page := range spotCtx.Pages {
		tracks = append(tracks, page.Tracks...)
	}
	return tracks
}

// Context resolve leaves the episodes of a playlist out when it mixes them
// with tracks. The playlist service lists them, and the rest of the context is
// what context resolve would have given: the same uids and metadata.
func (suite *RequestSuite) TestContextResolveBuildsPlaylistsFromThePlaylistService() {
	suite.answerWith(protoAnswer(suite, &playlist4pb.SelectedListContent{
		Revision: []byte{0x00, 0x00, 0x00, 0x02, 0x41, 0xc1},
		Length:   proto.Int32(3),
		Attributes: &playlist4pb.ListAttributes{
			Name:        proto.String("Mixed"),
			Description: proto.String("tracks and an episode"),
		},
		Contents: &playlist4pb.ListItems{Pos: proto.Int32(0), Truncated: proto.Bool(false), Items: []*playlist4pb.Item{
			playlistItem("spotify:track:1BdI8NEvZx61tnkbuAxC5x", []byte{0xeb, 0x5d, 0xea, 0x87, 0xec, 0x6c, 0x22, 0x0e}, "someone"),
			playlistItem("spotify:track:4u615SPuJfGtPaEUSKwuK4", []byte{0x0f, 0x79, 0x5e, 0x0a, 0x7c, 0x88, 0x2e, 0x5b}, "someone"),
			playlistItem("spotify:episode:4IzpgR6RCEkRqMHbJF38Wp", []byte{0xba, 0x52, 0xee, 0x4c, 0xa0, 0x64, 0x10, 0x89}, "someone"),
		}},
		OwnerUsername: proto.String("someone"),
	}))

	spotCtx, err := suite.spclient.ContextResolve(suite.T().Context(), testPlaylistUri)
	suite.Require().NoError(err)

	suite.Require().Len(suite.requests(), 1)
	suite.Equal("/playlist/v2/playlist/4csZj4lePVm2yAf5Lg7ZXt", suite.requests()[0].path)
	suite.Equal("0", suite.requests()[0].query.Get("from"))
	suite.Equal("50", suite.requests()[0].query.Get("length"))

	suite.Equal(testPlaylistUri, spotCtx.Uri)
	suite.Equal("context://"+testPlaylistUri, spotCtx.Url)
	suite.Equal(map[string]string{
		"context_description":      "Mixed",
		"context_long_description": "Mixed",
		"context_owner":            "someone",
		"playlist.revision":        "0000000241c1",
	}, spotCtx.Metadata)

	tracks := contextTracks(spotCtx)
	suite.Require().Len(tracks, 3)
	suite.Equal("spotify:episode:4IzpgR6RCEkRqMHbJF38Wp", tracks[2].Uri)
	suite.Equal("eb5dea87ec6c220e", tracks[0].Uid)
	suite.Equal(map[string]string{
		"added_at":          "1791031004968",
		"added_by_username": "someone",
		"highlight_id":      "eb5dea87ec6c220e",
	}, tracks[0].Metadata)
}

// An editorial playlist carries its format and format attributes, which context
// resolve hands on as context metadata, and so do its items. Nobody in
// particular added those items: they count as added by the owner.
func (suite *RequestSuite) TestPlaylistContextCarriesFormatAttributes() {
	suite.answerWith(protoAnswer(suite, &playlist4pb.SelectedListContent{
		Length: proto.Int32(1),
		Attributes: &playlist4pb.ListAttributes{
			Name:   proto.String("New Music Friday"),
			Format: proto.String("format-shows-shuffle"),
			FormatAttributes: []*playlist4pb.FormatListAttribute{
				{Key: proto.String("image_url"), Value: proto.String("https://i.scdn.co/image/x")},
				{Key: proto.String("editorial.series"), Value: proto.String("nmf")},
			},
		},
		Contents: &playlist4pb.ListItems{Pos: proto.Int32(0), Truncated: proto.Bool(false), Items: []*playlist4pb.Item{
			playlistItem("spotify:track:2VXgwSL3jCwYrnTh47mes8", []byte("SFpgHPnWzgs"), "",
				&playlist4pb.FormatListAttribute{Key: proto.String("decision_id"), Value: proto.String("ssp~1")}),
		}},
		OwnerUsername: proto.String("spotify"),
	}))

	spotCtx, err := suite.spclient.ContextResolve(suite.T().Context(), testPlaylistUri)
	suite.Require().NoError(err)

	suite.Equal("format-shows-shuffle", spotCtx.Metadata["format_list_type"])
	suite.Equal("https://i.scdn.co/image/x", spotCtx.Metadata["image_url"])
	suite.Equal("nmf", spotCtx.Metadata["editorial.series"])
	suite.NotContains(spotCtx.Metadata, "context_long_description", "no description, no long description")

	track := contextTracks(spotCtx)[0]
	suite.Equal("5346706748506e577a6773", track.Uid)
	suite.Equal("spotify", track.Metadata["added_by_username"])
	suite.Equal("ssp~1", track.Metadata["decision_id"])
}

// Only the first page of a playlist is fetched up front. The page after it is
// fetched when playback gets there, from where the first one ended.
func (suite *RequestSuite) TestPlaylistContextFetchesLaterPagesLazily() {
	suite.answerWith(
		protoAnswer(suite, &playlist4pb.SelectedListContent{
			Length: proto.Int32(3),
			Contents: &playlist4pb.ListItems{Pos: proto.Int32(0), Truncated: proto.Bool(true), Items: []*playlist4pb.Item{
				playlistItem("spotify:track:1BdI8NEvZx61tnkbuAxC5x", []byte{1}, "someone"),
				playlistItem("spotify:track:4u615SPuJfGtPaEUSKwuK4", []byte{2}, "someone"),
			}},
			OwnerUsername: proto.String("someone"),
		}),
		protoAnswer(suite, &playlist4pb.SelectedListContent{
			Length: proto.Int32(3),
			Contents: &playlist4pb.ListItems{Pos: proto.Int32(2), Truncated: proto.Bool(false), Items: []*playlist4pb.Item{
				playlistItem("spotify:episode:4IzpgR6RCEkRqMHbJF38Wp", []byte{3}, ""),
			}},
			OwnerUsername: proto.String("someone"),
		}),
	)

	resolver, err := spclient.NewContextResolver(suite.T().Context(), &librespot.NullLogger{}, suite.spclient, &connectpb.Context{Uri: testPlaylistUri})
	suite.Require().NoError(err)
	suite.Require().Len(suite.requests(), 1, "only the first page up front")

	first, err := resolver.Page(suite.T().Context(), 0)
	suite.Require().NoError(err)
	suite.Len(first, 2)
	suite.Len(suite.requests(), 1)

	second, err := resolver.Page(suite.T().Context(), 1)
	suite.Require().NoError(err)
	suite.Require().Len(second, 1)
	suite.Equal("spotify:episode:4IzpgR6RCEkRqMHbJF38Wp", second[0].Uri)
	suite.Equal("someone", second[0].Metadata["added_by_username"])

	suite.Require().Len(suite.requests(), 2)
	suite.Equal("/playlist/v2/playlist/4csZj4lePVm2yAf5Lg7ZXt", suite.requests()[1].path)
	suite.Equal("2", suite.requests()[1].query.Get("from"))
	suite.Equal("50", suite.requests()[1].query.Get("length"))

	_, err = resolver.Page(suite.T().Context(), 2)
	suite.ErrorIs(err, io.EOF, "the last page names no next one")
}

// A playlist with a picture of its own has it as its image. The items of a
// generated playlist, like a daily mix, carry no time: context resolve gives
// them no added_at, rather than a zero one.
func (suite *RequestSuite) TestPlaylistContextPictureAndUntimedItems() {
	item := playlistItem("spotify:track:2VXgwSL3jCwYrnTh47mes8", []byte("0550879"), "")
	item.Attributes.Timestamp = nil

	suite.answerWith(protoAnswer(suite, &playlist4pb.SelectedListContent{
		Length: proto.Int32(1),
		Attributes: &playlist4pb.ListAttributes{
			Name:    proto.String("Power Hits"),
			Picture: []byte{0xab, 0x67, 0x70, 0x6c, 0x00, 0x00},
		},
		Contents:      &playlist4pb.ListItems{Pos: proto.Int32(0), Truncated: proto.Bool(false), Items: []*playlist4pb.Item{item}},
		OwnerUsername: proto.String("spotify"),
	}))

	spotCtx, err := suite.spclient.ContextResolve(suite.T().Context(), testPlaylistUri)
	suite.Require().NoError(err)

	suite.Equal("https://u.scdn.co/images/pl/default/ab67706c0000", spotCtx.Metadata["image_url"])
	suite.NotContains(contextTracks(spotCtx)[0].Metadata, "added_at")
}

// Whatever the playlist service will not answer is left to context resolve,
// so a refused playlist still fails the way callers expect.
func (suite *RequestSuite) TestContextResolveFallsBackForPlaylists() {
	suite.answerWith(
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) },
		func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"uri":"` + testPlaylistUri + `","pages":[{"tracks":[{"uri":"spotify:track:1BdI8NEvZx61tnkbuAxC5x"}]}]}`))
		},
	)

	spotCtx, err := suite.spclient.ContextResolve(suite.T().Context(), testPlaylistUri)
	suite.Require().NoError(err)
	suite.Len(contextTracks(spotCtx), 1)

	suite.Require().Len(suite.requests(), 2)
	suite.Equal("/context-resolve/v1/"+testPlaylistUri, suite.requests()[1].path)
}
