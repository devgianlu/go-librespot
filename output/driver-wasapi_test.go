//go:build windows && test_unit

package output

import (
	"strings"
	"testing"
)

type silenceReader struct{}

func (silenceReader) Read(p []float32) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestWasapiRequiresReader(t *testing.T) {
	_, err := NewOutput(&NewOutputOptions{
		Backend:      "wasapi",
		SampleRate:   44100,
		ChannelCount: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "reader") {
		t.Fatalf("error = %v, want reader required", err)
	}
}

func TestWasapiDefaultDevice(t *testing.T) {
	out, err := NewOutput(&NewOutputOptions{
		Backend:      "wasapi",
		Reader:       silenceReader{},
		SampleRate:   44100,
		ChannelCount: 2,
		VolumeUpdate: make(chan float32, 1),
	})
	if err != nil {
		t.Skipf("no usable default audio endpoint: %v", err)
	}
	defer func() { _ = out.Close() }()

	if err := out.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := out.Drop(); err != nil {
		t.Fatalf("Drop while paused: %v", err)
	}
	if delay, err := out.DelayMs(); err != nil {
		t.Fatalf("DelayMs: %v", err)
	} else if delay != 0 {
		t.Fatalf("DelayMs while paused = %d, want 0", delay)
	}
	if err := out.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := out.Drop(); err != nil {
		t.Fatalf("Drop while playing: %v", err)
	}
	out.SetVolume(0.5)
}
