//go:build !android && !darwin && !js && !windows && !nintendosdk

package output

// #cgo pkg-config: alsa
//
// #include <alsa/asoundlib.h>
import "C"
import (
	"errors"
	"fmt"
	librespot "go-librespot"
	"golang.org/x/sys/unix"
	"io"
	"sync"
	"unsafe"
)

const (
	DisableHardwarePause = true // FIXME: should we fix this?
	ReleasePcmOnPause    = true
)

type output struct {
	channels   int
	sampleRate int
	device     string
	reader     librespot.Float32Reader

	cond *sync.Cond

	pcmHandle *C.snd_pcm_t
	canPause  bool

	volume   float32
	paused   bool
	closed   bool
	released bool

	err chan error
}

func newOutput(reader librespot.Float32Reader, sampleRate int, channels int, device string, initialVolume float32) (*output, error) {
	out := &output{
		reader:     reader,
		channels:   channels,
		sampleRate: sampleRate,
		device:     device,
		volume:     initialVolume,
		err:        make(chan error, 1),
		cond:       sync.NewCond(&sync.Mutex{}),
	}

	if err := out.openAndSetup(); err != nil {
		return nil, err
	}

	go func() {
		out.err <- out.loop()
		_ = out.Close()
	}()

	return out, nil
}

func (out *output) alsaError(name string, err C.int) error {
	if errors.Is(unix.Errno(-err), unix.EPIPE) {
		_ = out.Close()
	}

	return fmt.Errorf("ALSA error at %s: %s", name, C.GoString(C.snd_strerror(err)))
}

func (out *output) openAndSetup() error {
	cdevice := C.CString(out.device)
	defer C.free(unsafe.Pointer(cdevice))
	if err := C.snd_pcm_open(&out.pcmHandle, cdevice, C.SND_PCM_STREAM_PLAYBACK, 0); err < 0 {
		return out.alsaError("snd_pcm_open", err)
	}

	var params *C.snd_pcm_hw_params_t
	C.snd_pcm_hw_params_malloc(&params)
	defer C.free(unsafe.Pointer(params))

	if err := C.snd_pcm_hw_params_any(out.pcmHandle, params); err < 0 {
		return out.alsaError("snd_pcm_hw_params_any", err)
	}

	if err := C.snd_pcm_hw_params_set_access(out.pcmHandle, params, C.SND_PCM_ACCESS_RW_INTERLEAVED); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_access", err)
	}

	if err := C.snd_pcm_hw_params_set_format(out.pcmHandle, params, C.SND_PCM_FORMAT_FLOAT_LE); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_format", err)
	}

	if err := C.snd_pcm_hw_params_set_channels(out.pcmHandle, params, C.unsigned(out.channels)); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_channels", err)
	}

	if err := C.snd_pcm_hw_params_set_rate_resample(out.pcmHandle, params, 1); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_rate_resample", err)
	}

	sr := C.unsigned(out.sampleRate)
	if err := C.snd_pcm_hw_params_set_rate_near(out.pcmHandle, params, &sr, nil); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_rate_near", err)
	}

	if err := C.snd_pcm_hw_params(out.pcmHandle, params); err < 0 {
		return out.alsaError("snd_pcm_hw_params", err)
	}

	if DisableHardwarePause {
		out.canPause = false
	} else {
		if err := C.snd_pcm_hw_params_can_pause(params); err < 0 {
			return out.alsaError("snd_pcm_hw_params_can_pause", err)
		} else {
			out.canPause = err == 1
		}
	}

	return nil
}

func (out *output) loop() error {
	floats := make([]float32, out.channels*16*1024)

	for {
		n, err := out.reader.Read(floats)
		if errors.Is(err, io.EOF) || errors.Is(err, librespot.ErrDrainReader) {
			// drain pcm ignoring errors
			out.cond.L.Lock()
			C.snd_pcm_drain(out.pcmHandle)
			out.cond.L.Unlock()

			if errors.Is(err, io.EOF) {
				return nil
			}

			out.reader.Drained()
			continue
		} else if err != nil {
			return fmt.Errorf("failed reading source: %w", err)
		}

		if n%out.channels != 0 {
			return fmt.Errorf("invalid read amount: %d", n)
		}

		for i := 0; i < n; i++ {
			floats[i] *= out.volume
		}

		out.cond.L.Lock()
		for !(!out.paused || out.closed || !out.released) {
			out.cond.Wait()
		}

		if out.closed {
			out.cond.L.Unlock()
			return nil
		}

		if nn := C.snd_pcm_writei(out.pcmHandle, unsafe.Pointer(&floats[0]), C.snd_pcm_uframes_t(n/out.channels)); nn < 0 {
			nn = C.long(C.snd_pcm_recover(out.pcmHandle, C.int(nn), 1))
			if nn < 0 {
				out.cond.L.Unlock()
				return out.alsaError("snd_pcm_recover", C.int(nn))
			}
		}

		out.cond.L.Unlock()
	}
}

func (out *output) Pause() error {
	// Do not use snd_pcm_drop as this might hang (https://github.com/libsdl-org/SDL/blob/a5c610b0a3857d3138f3f3da1f6dc3172c5ea4a8/src/audio/alsa/SDL_alsa_audio.c#L478).

	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.paused {
		return nil
	}

	if ReleasePcmOnPause {
		if !out.released {
			if err := C.snd_pcm_close(out.pcmHandle); err < 0 {
				return out.alsaError("snd_pcm_close", err)
			}
		}

		out.released = true
	} else if out.canPause {
		if C.snd_pcm_state(out.pcmHandle) != C.SND_PCM_STATE_RUNNING {
			return nil
		}

		if err := C.snd_pcm_pause(out.pcmHandle, 1); err < 0 {
			return out.alsaError("snd_pcm_pause", err)
		}
	}

	out.paused = true

	return nil
}

func (out *output) Resume() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || !out.paused {
		return nil
	}

	if ReleasePcmOnPause {
		if out.released {
			if err := out.openAndSetup(); err != nil {
				return err
			}
		}

		out.released = false
	} else if out.canPause {
		if C.snd_pcm_state(out.pcmHandle) != C.SND_PCM_STATE_PAUSED {
			return nil
		}

		if err := C.snd_pcm_pause(out.pcmHandle, 0); err < 0 {
			return out.alsaError("snd_pcm_pause", err)
		}
	}

	out.paused = false
	out.cond.Signal()

	return nil
}

func (out *output) Drop() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.released {
		return nil
	}

	if err := C.snd_pcm_drop(out.pcmHandle); err < 0 {
		return out.alsaError("snd_pcm_drop", err)
	}

	// since we are not actually stopping the stream, prepare it again
	if err := C.snd_pcm_prepare(out.pcmHandle); err < 0 {
		return out.alsaError("snd_pcm_prepare", err)
	}

	return nil
}

func (out *output) DelayMs() (int64, error) {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.released {
		return 0, nil
	}

	var frames C.snd_pcm_sframes_t
	if err := C.snd_pcm_delay(out.pcmHandle, &frames); err < 0 {
		return 0, out.alsaError("snd_pcm_delay", err)
	}

	return int64(frames) * 1000 / int64(out.sampleRate), nil
}

func (out *output) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}

	out.volume = vol
}

func (out *output) Error() <-chan error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	return out.err
}

func (out *output) Close() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.released {
		out.closed = true
		return nil
	}

	if err := C.snd_pcm_close(out.pcmHandle); err < 0 {
		return out.alsaError("snd_pcm_close", err)
	}

	out.closed = true
	out.cond.Signal()

	return nil
}
