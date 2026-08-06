//go:build test_unit

package zeroconf

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dh"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	"github.com/stretchr/testify/suite"
)

type HandlersSuite struct {
	suite.Suite

	zeroconf  *Zeroconf
	registrar *MockServiceRegistrar
}

func (suite *HandlersSuite) SetupTest() {
	// The mDNS backend is mocked rather than faked: these tests only care that
	// the registrar is told about a rename, not how it advertises.
	suite.registrar = NewMockServiceRegistrar(suite.T())

	suite.zeroconf = &Zeroconf{
		log:        &librespot.NullLogger{},
		reqsChan:   make(chan NewUserRequest, 1),
		deviceId:   "device-id",
		deviceName: "test device",
		deviceType: devicespb.DeviceType_SPEAKER,
		registrar:  suite.registrar,
	}

	var err error
	suite.zeroconf.dh, err = dh.NewDiffieHellman()
	suite.Require().NoError(err)
}

// addUserRequest builds a parsed form request, which is what the handler reads.
func (suite *HandlersSuite) addUserRequest(values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/?action=addUser", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	suite.Require().NoError(req.ParseForm())
	return req
}

func (suite *HandlersSuite) TestGetInfoDescribesTheDevice() {
	rec := httptest.NewRecorder()
	suite.Require().NoError(suite.zeroconf.handleGetInfo(rec, httptest.NewRequest(http.MethodGet, "/", nil)))

	suite.Equal(http.StatusOK, rec.Code)

	var got GetInfoResponse
	suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &got))

	suite.Equal("device-id", got.DeviceID)
	suite.Equal("test device", got.RemoteName)
	suite.Equal("SPEAKER", got.DeviceType)
	suite.Empty(got.ActiveUser, "no user is connected yet")

	// Spotify clients key their handshake off this, so it has to be the real
	// public key in base64.
	key, err := base64.StdEncoding.DecodeString(got.PublicKey)
	suite.Require().NoError(err)
	suite.Equal(suite.zeroconf.dh.PublicKeyBytes(), key)

	// These are what make a client willing to talk to us at all.
	suite.Equal(101, got.Status)
	suite.Equal("OK", got.StatusString)
	suite.Equal("PREMIUM", got.AccountReq)
}

func (suite *HandlersSuite) TestGetInfoReportsTheCurrentUser() {
	suite.zeroconf.SetCurrentUser("someone")

	rec := httptest.NewRecorder()
	suite.Require().NoError(suite.zeroconf.handleGetInfo(rec, httptest.NewRequest(http.MethodGet, "/", nil)))

	var got GetInfoResponse
	suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	suite.Equal("someone", got.ActiveUser)
}

// The advertised name has to follow a rename, since that is the whole point of
// SetDeviceName.
func (suite *HandlersSuite) TestGetInfoReflectsRenames() {
	suite.registrar.EXPECT().UpdateName("kitchen").Return(nil).Once()

	suite.zeroconf.SetDeviceName("kitchen")

	rec := httptest.NewRecorder()
	suite.Require().NoError(suite.zeroconf.handleGetInfo(rec, httptest.NewRequest(http.MethodGet, "/", nil)))

	var got GetInfoResponse
	suite.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	suite.Equal("kitchen", got.RemoteName)
}

// A backend that cannot rename the service must not stop the daemon from
// adopting the new name: the failure is logged and playback continues.
func (suite *HandlersSuite) TestRenameSurvivesRegistrarFailure() {
	suite.registrar.EXPECT().UpdateName("kitchen").Return(errors.New("avahi is down")).Once()

	suite.Require().NotPanics(func() { suite.zeroconf.SetDeviceName("kitchen") })
	suite.Equal("kitchen", suite.zeroconf.deviceName)
}

// Every field is required; a half-filled form must be refused rather than
// half-processed.
func (suite *HandlersSuite) TestAddUserRequiresEveryField() {
	complete := url.Values{
		"userName":   []string{"someone"},
		"blob":       []string{base64.StdEncoding.EncodeToString([]byte("blob"))},
		"clientKey":  []string{base64.StdEncoding.EncodeToString([]byte("key"))},
		"deviceName": []string{"phone"},
	}

	for field, wantErr := range map[string]string{
		"userName":   "missing username",
		"blob":       "missing blob",
		"clientKey":  "missing client key",
		"deviceName": "missing device name",
	} {
		suite.Run("without "+field, func() {
			values := url.Values{}
			for k, v := range complete {
				if k != field {
					values[k] = v
				}
			}

			err := suite.zeroconf.handleAddUser(httptest.NewRecorder(), suite.addUserRequest(values))
			suite.Require().Error(err)
			suite.Contains(err.Error(), wantErr)
		})
	}
}

func (suite *HandlersSuite) TestAddUserRejectsUndecodableFields() {
	for _, tt := range []struct {
		name    string
		values  url.Values
		wantErr string
	}{
		{
			name: "blob is not base64",
			values: url.Values{
				"userName":   []string{"someone"},
				"blob":       []string{"!!!not base64!!!"},
				"clientKey":  []string{base64.StdEncoding.EncodeToString([]byte("key"))},
				"deviceName": []string{"phone"},
			},
			wantErr: "invalid blob",
		},
		{
			name: "client key is not base64",
			values: url.Values{
				"userName":   []string{"someone"},
				"blob":       []string{base64.StdEncoding.EncodeToString([]byte("blob"))},
				"clientKey":  []string{"!!!not base64!!!"},
				"deviceName": []string{"phone"},
			},
			wantErr: "invalid client key",
		},
	} {
		suite.Run(tt.name, func() {
			err := suite.zeroconf.handleAddUser(httptest.NewRecorder(), suite.addUserRequest(tt.values))
			suite.Require().Error(err)
			suite.Contains(err.Error(), tt.wantErr)
		})
	}
}

// The pairing endpoint is unauthenticated, so a blob too short to hold the IV
// and checksum has to be rejected rather than sliced: anyone on the network
// could otherwise panic the daemon with a single request.
func (suite *HandlersSuite) TestAddUserRejectsTruncatedBlob() {
	// 36 bytes is the smallest that still has no room for ciphertext.
	for _, size := range []int{1, 15, 16, 35, 36} {
		suite.Run(strconv.Itoa(size), func() {
			values := url.Values{
				"userName":   []string{"someone"},
				"blob":       []string{base64.StdEncoding.EncodeToString(make([]byte, size))},
				"clientKey":  []string{base64.StdEncoding.EncodeToString(make([]byte, 96))},
				"deviceName": []string{"phone"},
			}

			var err error
			suite.Require().NotPanics(func() {
				err = suite.zeroconf.handleAddUser(httptest.NewRecorder(), suite.addUserRequest(values))
			})
			suite.Require().Error(err)
			suite.Contains(err.Error(), "too short")
		})
	}
}

func TestHandlersSuite(t *testing.T) {
	suite.Run(t, new(HandlersSuite))
}
