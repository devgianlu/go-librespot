//go:build test_unit

package player

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
)

// The mp3 fixture stands in for a narration clip: same shape as the real thing,
// mono 44100Hz, which the decoder upmixes to stereo.
func narrationServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The clip url is pre-signed, so no credentials should be attached.
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("narration fetch sent Authorization: %q, want none", auth)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func narrationFixture(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile("../mp3/testdata/sine_mono.mp3")
	if err != nil {
		t.Fatalf("failed reading fixture: %v", err)
	}

	return data
}

func TestNarrationSourceDecodes(t *testing.T) {
	srv := narrationServer(t, http.StatusOK, narrationFixture(t))

	// Loudness and true peak as DJ declares them.
	src, err := NewNarrationSource(context.Background(), &librespot.NullLogger{},
		srv.Client(), srv.URL, -16.0, -3.0, 0)
	if err != nil {
		t.Fatalf("NewNarrationSource failed: %v", err)
	}
	defer func() { _ = src.Close() }()

	if src.SampleRate != SampleRate {
		t.Errorf("SampleRate = %d, want %d", src.SampleRate, SampleRate)
	}
	if src.Channels != Channels {
		t.Errorf("Channels = %d, want %d", src.Channels, Channels)
	}

	// It has to satisfy the interface the player consumes.
	var _ librespot.AudioSource = src

	var frames int
	buf := make([]float32, 4096)
	for {
		n, err := src.Read(buf)
		frames += n / 2
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("read failed: %v", err)
		}
	}

	if frames == 0 {
		t.Fatal("decoded no audio")
	}
}

// -16 LUFS against the -14 target is a small boost, and the -3 dBTP peak leaves
// room for it, so the clip should come out louder rather than clamped.
func TestNarrationGainFromMetadata(t *testing.T) {
	got := normalisationFactorFor(-16.0, -3.0, 0)
	if got <= 1.0 || got >= 1.6 {
		t.Errorf("gain = %f, want a modest boost above 1", got)
	}

	// A very loud clip must be pulled down so its peak cannot clip.
	if clamped := normalisationFactorFor(-30.0, -0.1, 0); clamped > 1.05 {
		t.Errorf("gain = %f, want clamping near unity for a hot peak", clamped)
	}
}

func TestNarrationLoudness(t *testing.T) {
	md := map[string]string{
		"narration.intro.loudness":  "-16.0",
		"narration.intro.true_peak": "-3.0",
	}

	loudness, truePeak, ok := NarrationLoudness(md, "narration.intro")
	if !ok || loudness != -16.0 || truePeak != -3.0 {
		t.Errorf("NarrationLoudness = %v, %v, %v; want -16, -3, true", loudness, truePeak, ok)
	}

	// Absent or malformed metadata must be reported, not silently taken as zero,
	// since 0 LUFS would mean a huge attenuation.
	if _, _, ok := NarrationLoudness(map[string]string{}, "narration.intro"); ok {
		t.Error("NarrationLoudness reported ok for missing metadata")
	}
	if _, _, ok := NarrationLoudness(map[string]string{
		"narration.intro.loudness":  "loud",
		"narration.intro.true_peak": "-3.0",
	}, "narration.intro"); ok {
		t.Error("NarrationLoudness reported ok for unparseable loudness")
	}
}

func TestNarrationSourceErrors(t *testing.T) {
	ctx := context.Background()
	log := &librespot.NullLogger{}

	t.Run("http error", func(t *testing.T) {
		srv := narrationServer(t, http.StatusNotFound, nil)
		if _, err := NewNarrationSource(ctx, log, srv.Client(), srv.URL, -16, -3, 0); err == nil {
			t.Error("expected an error for a 404")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		srv := narrationServer(t, http.StatusOK, nil)
		if _, err := NewNarrationSource(ctx, log, srv.Client(), srv.URL, -16, -3, 0); err == nil {
			t.Error("expected an error for an empty clip")
		}
	})

	t.Run("not mp3", func(t *testing.T) {
		srv := narrationServer(t, http.StatusOK, []byte(strings.Repeat("not audio", 64)))
		if _, err := NewNarrationSource(ctx, log, srv.Client(), srv.URL, -16, -3, 0); err == nil {
			t.Error("expected an error for a non-mp3 body")
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		if _, err := NewNarrationSource(ctx, log, http.DefaultClient,
			"http://127.0.0.1:1/nope", -16, -3, 0); err == nil {
			t.Error("expected an error for an unreachable host")
		}
	})
}
