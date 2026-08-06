//go:build test_unit

package flac_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/flac"
)

// The API reports bit depth from the decoder, so it has to come off STREAMINFO.
func TestDecoderStreamInfo(t *testing.T) {
	data, err := os.ReadFile("testdata/sine16.flac")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	d, err := flac.New(&librespot.NullLogger{}, bytes.NewReader(data), 1.0)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}
	defer func() { _ = d.Close() }()

	if d.BitDepth != 16 {
		t.Errorf("BitDepth = %d, want 16", d.BitDepth)
	}
	if d.SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100", d.SampleRate)
	}
	if d.Channels != 2 {
		t.Errorf("Channels = %d, want 2", d.Channels)
	}
}

func TestDecoderFullScalePeak(t *testing.T) {
	data, err := os.ReadFile("testdata/sine16.flac")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	d, err := flac.New(&librespot.NullLogger{}, bytes.NewReader(data), 1.0)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}
	defer func() { _ = d.Close() }()

	var peak float32
	buf := make([]float32, 4096)
	for {
		n, err := d.Read(buf)
		for _, v := range buf[:n] {
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("failed to read samples: %v", err)
		}
	}

	want := float32(32767) / float32(32768)
	if peak != want {
		t.Fatalf("peak = %f, want %f", peak, want)
	}
}
