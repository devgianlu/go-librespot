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
	"io"
	"sync"
	"unsafe"
)

const DisableHardwarePause = true // FIXME: should we fix this?

type output struct {
	channels int
	reader   librespot.Float32Reader
	done     chan error

	cond *sync.Cond

	handle   *C.snd_pcm_t
	canPause bool

	volume float32
	paused bool
	closed bool
	eof    bool
}

func alsaError(name string, err C.int) error {
	return fmt.Errorf("ALSA error at %s: %s", name, C.GoString(C.snd_strerror(err)))
}

func newOutput(reader librespot.Float32Reader, sampleRate int, channels int, device string) (*output, error) {
	out := &output{
		reader:   reader,
		channels: channels,
		volume:   1,
		cond:     sync.NewCond(&sync.Mutex{}),
		done:     make(chan error, 1),
	}

	cdevice := C.CString(device)
	defer C.free(unsafe.Pointer(cdevice))
	if err := C.snd_pcm_open(&out.handle, cdevice, C.SND_PCM_STREAM_PLAYBACK, 0); err < 0 {
		return nil, alsaError("snd_pcm_open", err)
	}

	if err := out.alsaPcmHwParams(sampleRate, channels); err != nil {
		return nil, err
	}

	go func() {
		err := out.loop()
		_ = out.Close()

		if err != nil {
			out.done <- err
		} else {
			out.done <- nil
		}
	}()

	return out, nil
}

func (out *output) alsaPcmHwParams(sampleRate, channelCount int) error {
	var params *C.snd_pcm_hw_params_t
	C.snd_pcm_hw_params_malloc(&params)
	defer C.free(unsafe.Pointer(params))

	if err := C.snd_pcm_hw_params_any(out.handle, params); err < 0 {
		return alsaError("snd_pcm_hw_params_any", err)
	}

	if err := C.snd_pcm_hw_params_set_access(out.handle, params, C.SND_PCM_ACCESS_RW_INTERLEAVED); err < 0 {
		return alsaError("snd_pcm_hw_params_set_access", err)
	}

	if err := C.snd_pcm_hw_params_set_format(out.handle, params, C.SND_PCM_FORMAT_FLOAT_LE); err < 0 {
		return alsaError("snd_pcm_hw_params_set_format", err)
	}

	if err := C.snd_pcm_hw_params_set_channels(out.handle, params, C.unsigned(channelCount)); err < 0 {
		return alsaError("snd_pcm_hw_params_set_channels", err)
	}

	if err := C.snd_pcm_hw_params_set_rate_resample(out.handle, params, 1); err < 0 {
		return alsaError("snd_pcm_hw_params_set_rate_resample", err)
	}

	sr := C.unsigned(sampleRate)
	if err := C.snd_pcm_hw_params_set_rate_near(out.handle, params, &sr, nil); err < 0 {
		return alsaError("snd_pcm_hw_params_set_rate_near", err)
	}

	if err := C.snd_pcm_hw_params(out.handle, params); err < 0 {
		return alsaError("snd_pcm_hw_params", err)
	}

	if DisableHardwarePause {
		out.canPause = false
	} else {
		if err := C.snd_pcm_hw_params_can_pause(params); err < 0 {
			return alsaError("snd_pcm_hw_params_can_pause", err)
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
		if errors.Is(err, io.EOF) {
			out.eof = true
			return nil
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
		for out.paused && !out.closed {
			out.cond.Wait()
		}

		if out.closed {
			out.cond.L.Unlock()
			return nil
		}

		if nn := C.snd_pcm_writei(out.handle, unsafe.Pointer(&floats[0]), C.snd_pcm_uframes_t(n/out.channels)); nn < 0 {
			nn = C.long(C.snd_pcm_recover(out.handle, C.int(nn), 1))
			if nn < 0 {
				out.cond.L.Unlock()
				return alsaError("snd_pcm_recover", C.int(nn))
			}
		}

		out.cond.L.Unlock()
	}
}

func (out *output) Pause() error {
	// Do not use snd_pcm_drop as this might hang (https://github.com/libsdl-org/SDL/blob/a5c610b0a3857d3138f3f3da1f6dc3172c5ea4a8/src/audio/alsa/SDL_alsa_audio.c#L478).

	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed {
		return nil
	}

	if out.canPause {
		if C.snd_pcm_state(out.handle) != C.SND_PCM_STATE_RUNNING {
			return nil
		}

		if err := C.snd_pcm_pause(out.handle, 1); err < 0 {
			return alsaError("snd_pcm_pause", err)
		}
	}

	out.paused = true

	return nil
}

func (out *output) Resume() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed {
		return nil
	}

	if out.canPause {
		if C.snd_pcm_state(out.handle) != C.SND_PCM_STATE_PAUSED {
			return nil
		}

		if err := C.snd_pcm_pause(out.handle, 0); err < 0 {
			return alsaError("snd_pcm_pause", err)
		}
	}

	out.paused = false
	out.cond.Signal()

	return nil
}

func (out *output) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}

	out.volume = vol
}

func (out *output) WaitDone() <-chan error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed {
		return nil
	}

	return out.done
}

func (out *output) IsEOF() bool {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	return out.eof
}

func (out *output) Close() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed {
		return nil
	}

	if err := C.snd_pcm_close(out.handle); err < 0 {
		return alsaError("snd_pcm_close", err)
	}

	out.closed = true
	out.cond.Signal()

	return nil
}
