//go:build test_unit && test_integration

package player

import (
	"io"
	"os"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
)

// Set GO_LIBRESPOT_TEST_PULSE_SINK to a disposable PulseAudio or PipeWire
// null sink. This drives the same primary-stream handoff used by the daemon.
func testPulsePlayer(t *testing.T) *Player {
	t.Helper()
	sink := os.Getenv("GO_LIBRESPOT_TEST_PULSE_SINK")
	if sink == "" {
		t.Skip("requires a disposable Pulse sink")
	}
	p, err := NewPlayer(&Options{
		Log: &librespot.NullLogger{}, AudioBackend: "pulseaudio",
		AudioDevice: sink, VolumeUpdate: make(chan float32, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestPulseNaturalTrackEnd(t *testing.T) {
	p := testPulsePlayer(t)
	const duration = 4 * time.Second
	for i := 0; i < 3; i++ {
		src := constSource(4*SampleRate*Channels, float32(i+1)/16, 4096)
		src.eofWithLastRead = true
		start := time.Now()
		if err := p.SetPrimaryStream(src, false, false); err != nil {
			t.Fatal(err)
		}
		waitPulseTrackEnd(t, p)
		if elapsed := time.Since(start); elapsed < duration-250*time.Millisecond {
			t.Fatalf("track %d ended after %v, want %v", i+1, elapsed, duration)
		}
	}
}

func waitPulseTrackEnd(t *testing.T, p *Player) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-p.Receive():
			if ev.Type == EventTypeNotPlaying {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the end of the track")
		}
	}
}

// An explicit command received after the decoder reaches EOF must still take
// effect immediately, and a previous drain must never end the replacement.
func TestPulseCommandDuringDrain(t *testing.T) {
	for _, command := range []string{"skip", "seek", "pause", "reopen"} {
		t.Run(command, func(t *testing.T) {
			p := testPulsePlayer(t)
			src := &eofSignalSource{
				fakeSource: constSource(4*SampleRate*Channels, 0.25, 4096),
				eof:        make(chan struct{}),
			}
			if err := p.SetPrimaryStream(src, false, false); err != nil {
				t.Fatal(err)
			}
			select {
			case <-src.eof:
			case <-time.After(6 * time.Second):
				t.Fatal("decoder did not reach EOF")
			}

			start := time.Now()
			var err error
			switch command {
			case "skip":
				err = p.SetPrimaryStream(constSource(4*SampleRate*Channels, 0.5, 4096), false, true)
			case "seek":
				err = p.SeekMs(0)
			case "pause":
				err = p.Pause()
			case "reopen":
				err = p.ReopenOutput(os.Getenv("GO_LIBRESPOT_TEST_PULSE_SINK"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("%s took %v", command, elapsed)
			}
			if command == "reopen" {
				waitPulseTrackEnd(t, p)
				return
			}
			if command == "pause" {
				pause := time.After(2200 * time.Millisecond)
				for {
					select {
					case ev := <-p.Receive():
						if ev.Type == EventTypeNotPlaying {
							t.Fatal("track advanced while paused")
						}
					case <-pause:
						if err := p.Play(); err != nil {
							t.Fatal(err)
						}
						waitPulseTrackEnd(t, p)
						return
					}
				}
			}
			waitPulseTrackEnd(t, p)
			if elapsed := time.Since(start); elapsed < 3750*time.Millisecond {
				t.Fatalf("%s delivered a stale end event after %v", command, elapsed)
			}
		})
	}
}

type eofSignalSource struct {
	*fakeSource
	once sync.Once
	eof  chan struct{}
}

func (s *eofSignalSource) Read(p []float32) (int, error) {
	n, err := s.fakeSource.Read(p)
	if err == io.EOF {
		s.once.Do(func() { close(s.eof) })
	}
	return n, err
}
