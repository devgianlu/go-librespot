//go:build test_unit

package spclient_test

import (
	"net/http"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/spclient"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

// Context resolve answers a bare track uri with the uri and no pages; the
// context is then that one track (devgianlu/go-librespot#422).
func (suite *RequestSuite) TestContextResolverPlaysABareTrack() {
	const uri = "spotify:track:197PHOBtgwBTSky9oye0u9"
	suite.answerWith(func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"uri":"` + uri + `"}`))
	})

	r, err := spclient.NewContextResolver(suite.T().Context(), &librespot.NullLogger{}, suite.spclient, &connectpb.Context{Uri: uri})
	suite.Require().NoError(err)

	tracks, err := r.Page(suite.T().Context(), 0)
	suite.Require().NoError(err)
	suite.Require().Len(tracks, 1)
	suite.Equal(uri, tracks[0].Uri)
}

// A collection that resolves to nothing fails to load instead of panicking.
func (suite *RequestSuite) TestContextResolverRejectsAnEmptyCollection() {
	const uri = "spotify:album:2up3OPMp9Tb4dAKM2erWXQ"
	suite.answerWith(func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"uri":"` + uri + `","pages":[]}`))
	})

	_, err := spclient.NewContextResolver(suite.T().Context(), &librespot.NullLogger{}, suite.spclient, &connectpb.Context{Uri: uri})
	suite.Error(err)
}
