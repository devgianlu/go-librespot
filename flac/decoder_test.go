package flac

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
)

func TestDecoderFullScalePeak(t *testing.T) {
	data, err := os.ReadFile("testdata/sine16.flac")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	d, err := New(&librespot.NullLogger{}, bytes.NewReader(data), 1.0)
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
