//go:build test_unit

package mp3_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/mp3"
)

// sine_mono.mp3 is a 0.5s 440Hz sine, 44100Hz, single channel, 320kbps.
func fixture(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile("testdata/sine_mono.mp3")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	return data
}

func newDecoder(t *testing.T, gain float32) *mp3.Decoder {
	t.Helper()

	d, err := mp3.New(&librespot.NullLogger{}, bytes.NewReader(fixture(t)), gain)
	if err != nil {
		t.Fatalf("failed to create decoder: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	return d
}

func drain(t *testing.T, d *mp3.Decoder) []float32 {
	t.Helper()

	var out []float32
	buf := make([]float32, 4096)
	for {
		n, err := d.Read(buf)
		out = append(out, buf[:n]...)
		if errors.Is(err, io.EOF) {
			return out
		} else if err != nil {
			t.Fatalf("failed to read samples: %v", err)
		}
	}
}

// A mono source must come out as stereo: the player rejects anything that is not
// two channels, and the narration clips are mono.
func TestDecoderUpmixesMonoToStereo(t *testing.T) {
	d := newDecoder(t, 1.0)

	if d.Channels != 2 {
		t.Fatalf("Channels = %d, want 2", d.Channels)
	}
	if d.SampleRate != 44100 {
		t.Fatalf("SampleRate = %d, want 44100", d.SampleRate)
	}

	samples := drain(t, d)
	if len(samples) == 0 {
		t.Fatal("decoded no samples")
	}
	if len(samples)%2 != 0 {
		t.Fatalf("got %d samples, want a whole number of stereo frames", len(samples))
	}

	// Both channels carry the same signal, so every frame must be identical.
	for i := 0; i < len(samples); i += 2 {
		if samples[i] != samples[i+1] {
			t.Fatalf("frame %d: L = %f, R = %f, want equal channels", i/2, samples[i], samples[i+1])
		}
	}
}

// Roughly half a second at 44100Hz stereo, allowing for encoder padding.
func TestDecoderDecodesExpectedLength(t *testing.T) {
	samples := drain(t, newDecoder(t, 1.0))

	frames := len(samples) / 2
	const want = 44100 / 2
	if frames < want*9/10 || frames > want*3/2 {
		t.Fatalf("decoded %d frames, want roughly %d", frames, want)
	}
}

func TestDecoderAppliesGain(t *testing.T) {
	peak := func(s []float32) float32 {
		var p float32
		for _, v := range s {
			if v < 0 {
				v = -v
			}
			if v > p {
				p = v
			}
		}
		return p
	}

	full := peak(drain(t, newDecoder(t, 1.0)))
	half := peak(drain(t, newDecoder(t, 0.5)))

	if full <= 0 {
		t.Fatal("full scale peak is zero")
	}

	// Gain is applied per sample, so halving it must halve the peak. Quantisation
	// means this is not exact.
	ratio := half / full
	if ratio < 0.49 || ratio > 0.51 {
		t.Fatalf("peak ratio = %f (full %f, half %f), want ~0.5", ratio, full, half)
	}
}

func TestDecoderSeek(t *testing.T) {
	d := newDecoder(t, 1.0)

	if pos := d.PositionMs(); pos != 0 {
		t.Fatalf("initial PositionMs = %d, want 0", pos)
	}

	if err := d.SetPositionMs(250); err != nil {
		t.Fatalf("failed seeking: %v", err)
	}

	// Byte offsets are exact here, so the reported position should round-trip.
	if pos := d.PositionMs(); pos != 250 {
		t.Fatalf("PositionMs after seek = %d, want 250", pos)
	}

	// Seeking forward must leave less audio behind than a full decode.
	rest := len(drain(t, d)) / 2
	if rest == 0 {
		t.Fatal("no samples left after seeking to the middle")
	}
	if full := len(drain(t, newDecoder(t, 1.0))) / 2; rest >= full {
		t.Fatalf("after seeking to 250ms got %d frames, want fewer than the full %d", rest, full)
	}
}

// Read must keep returning io.EOF once drained rather than erroring or blocking.
func TestDecoderReadAfterEOF(t *testing.T) {
	d := newDecoder(t, 1.0)
	drain(t, d)

	buf := make([]float32, 64)
	for i := 0; i < 3; i++ {
		n, err := d.Read(buf)
		if n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("read %d after EOF: n = %d, err = %v, want 0, io.EOF", i, n, err)
		}
	}
}

func TestDecoderReadEmptyBuffer(t *testing.T) {
	d := newDecoder(t, 1.0)

	n, err := d.Read(nil)
	if n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v, want 0, nil", n, err)
	}
}
