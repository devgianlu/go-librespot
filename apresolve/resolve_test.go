//go:build test_unit

package apresolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/suite"
)

type ApResolverSuite struct {
	suite.Suite

	server   *httptest.Server
	resolver *ApResolver

	// requests counts how many times the resolver actually hit the endpoint,
	// which is what distinguishes a cache hit from a refetch.
	requests atomic.Int32
	// queries records the ?type= values of each request, in order.
	queries [][]string
	// body is what the endpoint answers with; tests swap it per case.
	body string
	// status is the response code; tests override it to force failures.
	status int
}

func (suite *ApResolverSuite) SetupTest() {
	suite.requests.Store(0)
	suite.queries = nil
	suite.status = http.StatusOK
	suite.body = `{"accesspoint":["ap1:443","ap2:443"],"dealer":["dealer1:443"],"spclient":["sp1:443"]}`

	suite.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suite.requests.Add(1)
		suite.queries = append(suite.queries, r.URL.Query()["type"])

		w.WriteHeader(suite.status)
		_, _ = w.Write([]byte(suite.body))
	}))

	suite.resolver = NewApResolver(&librespot.NullLogger{}, suite.server.Client())

	// Point the resolver at the stub instead of the real endpoint.
	baseUrl, err := url.Parse(suite.server.URL)
	suite.Require().NoError(err)
	suite.resolver.baseUrl = baseUrl
}

func (suite *ApResolverSuite) TearDownTest() {
	suite.server.Close()
}

func (suite *ApResolverSuite) TestGetAccesspointReturnsFetchedAddresses() {
	get, err := suite.resolver.GetAccesspoint(suite.T().Context())
	suite.Require().NoError(err)

	suite.Equal("ap1:443", get(suite.T().Context()))
	suite.Equal("ap2:443", get(suite.T().Context()))
}

func (suite *ApResolverSuite) TestEachEndpointTypeIsServedSeparately() {
	ctx := suite.T().Context()

	ap, err := suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)
	dealer, err := suite.resolver.GetDealer(ctx)
	suite.Require().NoError(err)
	spclient, err := suite.resolver.GetSpclient(ctx)
	suite.Require().NoError(err)

	suite.Equal("ap1:443", ap(ctx))
	suite.Equal("dealer1:443", dealer(ctx))
	suite.Equal("sp1:443", spclient(ctx))
}

// Endpoints are cached for an hour, so a second lookup of the same type must
// not hit the network again.
func (suite *ApResolverSuite) TestCachedEndpointsAreNotRefetched() {
	ctx := suite.T().Context()

	_, err := suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)
	suite.Require().EqualValues(1, suite.requests.Load())

	_, err = suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)
	suite.Equal(int32(1), suite.requests.Load(), "second lookup should have been served from cache")
}

func (suite *ApResolverSuite) TestExpiredEndpointsAreRefetched() {
	ctx := suite.T().Context()

	_, err := suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)
	suite.Require().EqualValues(1, suite.requests.Load())

	suite.resolver.endpointsLock.Lock()
	suite.resolver.endpointsExp[endpointTypeAccesspoint] = time.Now().Add(-time.Minute)
	suite.resolver.endpointsLock.Unlock()

	_, err = suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)
	suite.Equal(int32(2), suite.requests.Load(), "an expired endpoint should have been refetched")
}

func (suite *ApResolverSuite) TestFetchAllRequestsEveryTypeAtOnce() {
	suite.Require().NoError(suite.resolver.FetchAll(suite.T().Context()))

	suite.Require().EqualValues(1, suite.requests.Load())
	suite.Require().Len(suite.queries, 1)
	suite.ElementsMatch([]string{"accesspoint", "dealer", "spclient"}, suite.queries[0])
}

// Once the caller has walked past the last address the resolver refetches and
// starts over, so a long lived session keeps rotating rather than pinning one
// endpoint forever.
func (suite *ApResolverSuite) TestAddressesRotateAndRefetchOnOverflow() {
	ctx := suite.T().Context()

	get, err := suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)

	suite.Equal("ap1:443", get(ctx))
	suite.Equal("ap2:443", get(ctx))

	// Exhausted: the next call refetches. Serve a different list to prove the
	// resolver adopted it rather than replaying the old one.
	suite.body = `{"accesspoint":["ap3:443","ap4:443"]}`
	suite.resolver.endpointsLock.Lock()
	suite.resolver.endpointsExp[endpointTypeAccesspoint] = time.Now().Add(-time.Minute)
	suite.resolver.endpointsLock.Unlock()

	suite.Equal("ap3:443", get(ctx))
	suite.Equal("ap4:443", get(ctx))
}

// A refetch that fails must not take playback down with it: the resolver falls
// back to an address it already knows.
func (suite *ApResolverSuite) TestOverflowFallsBackToFirstAddressWhenRefetchFails() {
	ctx := suite.T().Context()

	get, err := suite.resolver.GetAccesspoint(ctx)
	suite.Require().NoError(err)

	suite.Require().Equal("ap1:443", get(ctx))
	suite.Require().Equal("ap2:443", get(ctx))

	suite.status = http.StatusInternalServerError
	suite.resolver.endpointsLock.Lock()
	suite.resolver.endpointsExp[endpointTypeAccesspoint] = time.Now().Add(-time.Minute)
	suite.resolver.endpointsLock.Unlock()

	suite.Equal("ap1:443", get(ctx), "should reuse the first known address")
}

func (suite *ApResolverSuite) TestErrorsWhenEndpointReturnsNonOK() {
	suite.status = http.StatusServiceUnavailable

	_, err := suite.resolver.GetAccesspoint(suite.T().Context())
	suite.Require().Error(err)
	suite.Contains(err.Error(), "invalid status code")
}

func (suite *ApResolverSuite) TestErrorsOnMalformedResponse() {
	suite.body = "not json"

	_, err := suite.resolver.GetAccesspoint(suite.T().Context())
	suite.Require().Error(err)
	suite.Contains(err.Error(), "unmarhsalling")
}

// An empty list is a successful response that still cannot be used, and the
// caller has to be told rather than handed an empty address.
func (suite *ApResolverSuite) TestErrorsWhenNoEndpointsReturned() {
	suite.body = `{"accesspoint":[]}`

	_, err := suite.resolver.GetAccesspoint(suite.T().Context())
	suite.Require().Error(err)
	suite.Contains(err.Error(), "no accesspoint endpoint present")
}

func (suite *ApResolverSuite) TestErrorsWhenEndpointUnreachable() {
	suite.server.Close()

	_, err := suite.resolver.GetAccesspoint(suite.T().Context())
	suite.Require().Error(err)
	suite.Contains(err.Error(), "failed fetching apresolve URL")
}

// The context has to reach the HTTP call: a cancelled lookup should fail
// rather than block on a slow endpoint.
func (suite *ApResolverSuite) TestHonoursContextCancellation() {
	ctx, cancel := context.WithCancel(suite.T().Context())
	cancel()

	_, err := suite.resolver.GetAccesspoint(ctx)
	suite.Require().Error(err)
	suite.ErrorIs(err, context.Canceled)
}

func TestApResolverSuite(t *testing.T) {
	suite.Run(t, new(ApResolverSuite))
}
