//go:build darwin && test_unit

package output

import (
	"errors"
	"fmt"
	"io"
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

type toolboxScriptedReader struct {
	n   int
	err error
}

func (r toolboxScriptedReader) Read(samples []float32) (int, error) {
	for i := range samples[:r.n] {
		samples[i] = 0.5
	}
	return r.n, r.err
}

// The queue hands each of its few buffers back only through the callback,
// so a read must never leave one out: an empty or failed read has to play
// silence instead, or playback stutters once buffers are lost.
func TestAudioToolboxEveryReadFillsTheBuffer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reader  toolboxScriptedReader
		want    int
		silence bool
		failed  bool
	}{
		{"full", toolboxScriptedReader{n: 8}, 8, false, false},
		{"partial", toolboxScriptedReader{n: 3}, 3, false, false},
		{"empty at a track switch", toolboxScriptedReader{n: 0}, 8, true, false},
		{"failed", toolboxScriptedReader{n: 0, err: errors.New("boom")}, 8, true, true},
		{"last samples before EOF", toolboxScriptedReader{n: 5, err: io.EOF}, 5, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &toolboxOutput{reader: tc.reader, err: make(chan error, 1)}
			data := make([]float32, 8)
			for i := range data {
				data[i] = 1 // stale samples from the buffer's last use
			}
			got := out.readSamples(data)
			if got != tc.want {
				t.Fatalf("enqueues %d samples, want %d", got, tc.want)
			}
			for i, v := range data[:got] {
				if want := float32(0.5); tc.silence {
					want = 0
					if v != want {
						t.Fatalf("sample %d = %v, want silence", i, v)
					}
				} else if v != want {
					t.Fatalf("sample %d = %v, want the read sample", i, v)
				}
			}
			select {
			case err := <-out.Error():
				if !tc.failed {
					t.Fatalf("unexpected error %v", err)
				}
			default:
				if tc.failed {
					t.Fatal("the read error was not reported")
				}
			}
		})
	}
}
