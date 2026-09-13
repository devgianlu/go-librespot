//go:build darwin && test_unit

package output

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

type toolboxSilenceReader struct{ reads atomic.Int32 }

func (r *toolboxSilenceReader) Read(samples []float32) (int, error) {
	clear(samples)
	r.reads.Add(1)
	return len(samples), nil
}

// This test opens the real default audio device, but only submits silence.
// Opt in on a Mac with an output device; GOEXPERIMENT=cgocheck2 also verifies
// that callbacks never retain an unpinned Go pointer in C memory.
func TestAudioToolboxDefaultDevice(t *testing.T) {
	if os.Getenv("GO_LIBRESPOT_TEST_AUDIO_DEVICE") != "1" {
		t.Skip("set GO_LIBRESPOT_TEST_AUDIO_DEVICE=1 to open the default audio device")
	}
	for _, sampleRate := range []int{44100, 48000} {
		t.Run(fmt.Sprintf("%dHz", sampleRate), func(t *testing.T) {
			reader := new(toolboxSilenceReader)
			out, err := newAudioToolboxOutput(&NewOutputOptions{
				Reader: reader, SampleRate: sampleRate, ChannelCount: 2, InitialVolume: 0,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := out.Close(); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			})
			assertToolboxVolume(t, out, 0)
			waitToolboxRunning(t, out, true)
			waitToolbox(t, out, func() bool { return reader.reads.Load() > 0 })
			beforeGC := reader.reads.Load()
			runtime.GC()
			waitToolbox(t, out, func() bool { return reader.reads.Load() > beforeGC })
			for cycle := 0; cycle < 3; cycle++ {
				if err := out.Pause(); err != nil {
					t.Fatal(err)
				}
				// IsRunning describes the audio device and can stay true while
				// the queue is paused. Check reader consumption instead.
				// Allow callbacks already in flight to return before checking
				// that a paused queue is no longer consuming the reader.
				time.Sleep(100 * time.Millisecond)
				pausedReads := reader.reads.Load()
				time.Sleep(150 * time.Millisecond)
				if got := reader.reads.Load(); got != pausedReads {
					t.Fatalf("reader advanced while paused: %d -> %d", pausedReads, got)
				}
				if err := out.Resume(); err != nil {
					t.Fatal(err)
				}
				waitToolboxRunning(t, out, true)
				waitToolbox(t, out, func() bool { return reader.reads.Load() > pausedReads })
			}
			for _, volume := range []float32{0.25, 1, 0} {
				out.SetVolume(volume)
				assertToolboxVolume(t, out, volume)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
			if out.context != nil || out.audioQueue != nil {
				t.Fatal("Close retained the callback context or queue")
			}
			closedReads := reader.reads.Load()
			time.Sleep(100 * time.Millisecond)
			if got := reader.reads.Load(); got != closedReads {
				t.Fatalf("reader advanced after Close: %d -> %d", closedReads, got)
			}
			select {
			case err := <-out.Error():
				t.Fatalf("audio callback: %v", err)
			default:
			}
			// Cleanup calls Close again and checks its result too.
		})
	}
}

func assertToolboxVolume(t *testing.T, out *toolboxOutput, want float32) {
	t.Helper()
	if volume, err := toolboxVolumeForTest(out); err != nil || volume != want {
		t.Fatalf("native volume = %v, error = %v; want %v", volume, err, want)
	}
}

func waitToolboxRunning(t *testing.T, out *toolboxOutput, want bool) {
	t.Helper()
	waitToolbox(t, out, func() bool {
		running, err := toolboxRunningForTest(out)
		if err != nil {
			t.Fatal(err)
		}
		return running == want
	})
}

func waitToolbox(t *testing.T, out *toolboxOutput, condition func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !condition() {
		select {
		case err := <-out.Error():
			t.Fatalf("audio callback: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for the native audio queue")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAudioToolboxErrorDeliveryDoesNotBlock(t *testing.T) {
	out := &toolboxOutput{err: make(chan error, 1)}
	done := make(chan struct{})
	go func() {
		out.toolboxError("first", -1)
		out.toolboxError("second", -2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("error reporting blocked on a full channel")
	}
	if got := <-out.Error(); got.Error() != "first: -1" {
		t.Fatalf("first error was lost: %v", got)
	}
}
