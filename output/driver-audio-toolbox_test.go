//go:build darwin && test_unit

package output

import (
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
	reader := new(toolboxSilenceReader)
	out, err := newAudioToolboxOutput(&NewOutputOptions{
		Reader: reader, SampleRate: 44100, ChannelCount: 2, InitialVolume: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	if volume, err := toolboxVolumeForTest(out); err != nil || volume != 0 {
		t.Fatalf("initial native volume = %v, error = %v; want mute", volume, err)
	}
	runtime.GC()
	deadline := time.After(3 * time.Second)
	for reader.reads.Load() == 0 {
		select {
		case err := <-out.Error():
			t.Fatalf("audio callback: %v", err)
		case <-deadline:
			t.Fatal("the audio queue did not request any samples")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := out.Pause(); err != nil {
		t.Fatal(err)
	}
	if err := out.Resume(); err != nil {
		t.Fatal(err)
	}
	out.SetVolume(0.25)
	if volume, err := toolboxVolumeForTest(out); err != nil || volume != 0.25 {
		t.Fatalf("native volume = %v, error = %v; want 0.25", volume, err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
