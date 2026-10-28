//go:build test_unit

package player

import (
	"errors"
	"io"
	"testing"
	"time"
)

func TestSilentSourceReadsSilenceForItsDuration(t *testing.T) {
	src := newSilentSource(100)
	buf := make([]float32, 1024)
	for i := range buf {
		buf[i] = 1
	}

	start := time.Now()
	samples := 0
	for {
		n, err := src.Read(buf)
		for _, v := range buf[:n] {
			if v != 0 {
				t.Fatalf("read sample %v, want 0", v)
			}
		}
		samples += n
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	if want := 100 * SampleRate / 1000 * Channels; samples != want {
		t.Errorf("read %d samples, want %d", samples, want)
	}
	if got := src.PositionMs(); got != 100 {
		t.Errorf("PositionMs = %d, want 100", got)
	}
	// The last buffer is returned without waiting, so elapsed is about one buffer short of 100ms.
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Errorf("read 100ms of silence in %v, want real-time pacing", elapsed)
	}
}

func TestSilentSourceSetPositionClampsToDuration(t *testing.T) {
	src := newSilentSource(1000)

	_ = src.SetPositionMs(-50)
	if got := src.PositionMs(); got != 0 {
		t.Errorf("PositionMs after negative seek = %d, want 0", got)
	}

	_ = src.SetPositionMs(5000)
	if got := src.PositionMs(); got != 1000 {
		t.Errorf("PositionMs after seek past end = %d, want 1000", got)
	}
	if _, err := src.Read(make([]float32, 64)); !errors.Is(err, io.EOF) {
		t.Errorf("Read after seek past end: err = %v, want io.EOF", err)
	}
}
