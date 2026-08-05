package spclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	narrationpb "github.com/devgianlu/go-librespot/proto/spotify/narration"
	"google.golang.org/protobuf/proto"
)

// NarrationUrl turns a narration script into the url of its synthesized audio.
func (c *Spclient) NarrationUrl(ctx context.Context, req *narrationpb.TtsRequest) (string, error) {
	reqBody, err := proto.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed marshalling TtsRequest: %w", err)
	}

	resp, err := c.RequestNoRedirect(ctx, "POST", "/client-tts/v1/fulfill", nil, nil, reqBody)
	if err != nil {
		return "", err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("invalid status code from client tts: %d (%s)",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	url := resp.Header.Get("Location")
	if len(url) == 0 {
		return "", fmt.Errorf("missing location header from client tts")
	}

	return url, nil
}

func NewNarrationRequest(ssml string, voice narrationpb.ResolveRequest_TtsVoice,
	provider narrationpb.ResolveRequest_TtsProvider, sampleRate int32) *narrationpb.TtsRequest {
	return &narrationpb.TtsRequest{
		Prompt:       &narrationpb.TtsRequest_Ssml{Ssml: ssml},
		AudioFormat:  narrationpb.ResolveRequest_MP3,
		TtsVoice:     voice,
		TtsProvider:  provider,
		SampleRateHz: sampleRate,
	}
}
