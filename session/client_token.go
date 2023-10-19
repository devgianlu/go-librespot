package session

import (
	"bytes"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	pbdata "go-librespot/proto/spotify/clienttoken/data/v0"
	pbhttp "go-librespot/proto/spotify/clienttoken/http/v0"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/url"
)

func retrieveClientToken(deviceId string) (string, error) {
	body, err := proto.Marshal(&pbhttp.ClientTokenRequest{
		RequestType: pbhttp.ClientTokenRequestType_REQUEST_CLIENT_DATA_REQUEST,
		Request: &pbhttp.ClientTokenRequest_ClientData{
			ClientData: &pbhttp.ClientDataRequest{
				ClientId:      librespot.ClientId,
				ClientVersion: librespot.VersionString(),
				Data: &pbhttp.ClientDataRequest_ConnectivitySdkData{
					ConnectivitySdkData: &pbdata.ConnectivitySdkData{
						DeviceId:             deviceId,
						PlatformSpecificData: librespot.GetPlatformSpecificData(),
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed marshalling ClientTokenRequest: %w", err)
	}

	reqUrl, err := url.Parse("https://clienttoken.spotify.com/v1/clienttoken")
	if err != nil {
		return "", fmt.Errorf("invalid clienttoken url: %w", err)
	}

	resp, err := http.DefaultClient.Do(&http.Request{
		Method: "POST",
		URL:    reqUrl,
		Header: http.Header{
			"Accept":     []string{"application/x-protobuf"},
			"User-Agent": []string{librespot.UserAgent()},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	})
	if err != nil {
		return "", fmt.Errorf("failed requesting clienttoken: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed reading clienttoken response: %w", err)
	}

	var protoResp pbhttp.ClientTokenResponse
	if err := proto.Unmarshal(respBody, &protoResp); err != nil {
		return "", fmt.Errorf("faield unmarshalling clienttoken response: %w", err)
	}

	switch protoResp.ResponseType {
	case pbhttp.ClientTokenResponseType_RESPONSE_GRANTED_TOKEN_RESPONSE:
		token := protoResp.GetGrantedToken().Token
		log.Debugf("obtained new client token: %s", token)
		return token, nil
	case pbhttp.ClientTokenResponseType_RESPONSE_CHALLENGES_RESPONSE:
		return "", fmt.Errorf("clienttoken challenge not supported")
	default:
		return "", fmt.Errorf("unknown clienttoken response type: %v", protoResp.ResponseType)
	}
}
