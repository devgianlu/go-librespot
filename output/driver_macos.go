//go:build !android && !js && !windows && !nintendosdk && !linux && darwin

package output

//
// #include <CoreAudio/CoreAudio.h>
//
import "C"
import (
	"errors"
	"fmt"
	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	"io"
	"sync"
	"unsafe"
)

const (
	DisableHardwarePause = true // FIXME: should we fix this?
	ReleasePcmOnPause    = true
	BufferTimeMicro      = 500_000
)

type output struct {
	channels   int
	sampleRate int
	device     string
	reader     librespot.Float32Reader

	cond *sync.Cond

	pcmHandle  *C.snd_pcm_t
	canPause   bool
	periodSize int
	bufferSize int
	samples    *RingBuffer[[]float32]

	externalVolume bool

	volume   float32
	paused   bool
	closed   bool
	released bool

	externalVolumeUpdate *RingBuffer[float32]
	err                  chan error
}

func newOutput(reader librespot.Float32Reader, sampleRate int, channels int, device string, initialVolume float32, externalVolume bool, externalVolumeUpdate *RingBuffer[float32]) (*output, error) {
	out := &output{
		reader:               reader,
		channels:             channels,
		sampleRate:           sampleRate,
		device:               device,
		volume:               initialVolume,
		err:                  make(chan error, 2),
		cond:                 sync.NewCond(&sync.Mutex{}),
		externalVolume:       externalVolume,
		externalVolumeUpdate: externalVolumeUpdate,
	}

	if err := out.setupPcm(); err != nil {
		return nil, err
	}

	// this buffer holds multiple period buffers
	out.samples = NewRingBuffer[[]float32](16)

	go func() {
		out.err <- out.readLoop()
		_ = out.Close()
	}()

	go func() {
		out.err <- out.writeLoop()
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

func (out *output) setupPcm() error {
	cdevice := C.CString(out.device)
	defer C.free(unsafe.Pointer(cdevice))
	if err := C.snd_pcm_open(&out.pcmHandle, cdevice, C.SND_PCM_STREAM_PLAYBACK, 0); err < 0 {
		return out.alsaError("snd_pcm_open", err)
	}

	var hwparams *C.snd_pcm_hw_params_t
	C.snd_pcm_hw_params_malloc(&hwparams)
	defer C.free(unsafe.Pointer(hwparams))

	if err := C.snd_pcm_hw_params_any(out.pcmHandle, hwparams); err < 0 {
		return out.alsaError("snd_pcm_hw_params_any", err)
	}

	if err := C.snd_pcm_hw_params_set_access(out.pcmHandle, hwparams, C.SND_PCM_ACCESS_RW_INTERLEAVED); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_access", err)
	}

	if err := C.snd_pcm_hw_params_set_format(out.pcmHandle, hwparams, C.SND_PCM_FORMAT_FLOAT_LE); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_format", err)
	}

	if err := C.snd_pcm_hw_params_set_channels(out.pcmHandle, hwparams, C.unsigned(out.channels)); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_channels", err)
	}

	if err := C.snd_pcm_hw_params_set_rate_resample(out.pcmHandle, hwparams, 1); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_rate_resample", err)
	}

	sr := C.unsigned(out.sampleRate)
	if err := C.snd_pcm_hw_params_set_rate_near(out.pcmHandle, hwparams, &sr, nil); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_rate_near", err)
	}

	bufferTime := C.uint(BufferTimeMicro)
	if err := C.snd_pcm_hw_params_set_buffer_time_near(out.pcmHandle, hwparams, &bufferTime, nil); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_buffer_time_near", err)
	}

	if err := C.snd_pcm_hw_params(out.pcmHandle, hwparams); err < 0 {
		return out.alsaError("snd_pcm_hw_params", err)
	}

	_ = out.logParams(hwparams)

	if DisableHardwarePause {
		out.canPause = false
	} else {
		if err := C.snd_pcm_hw_params_can_pause(hwparams); err < 0 {
			return out.alsaError("snd_pcm_hw_params_can_pause", err)
		} else {
			out.canPause = err == 1
		}
	}

	var dir C.int
	var frames C.snd_pcm_uframes_t
	if err := C.snd_pcm_hw_params_get_period_size(hwparams, &frames, &dir); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_period_size", err)
	} else {
		out.periodSize = int(frames)
	}

	if err := C.snd_pcm_hw_params_get_buffer_size(hwparams, &frames); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_buffer_size", err)
	} else {
		out.bufferSize = int(frames)
	}

	var swparams *C.snd_pcm_sw_params_t
	C.snd_pcm_sw_params_malloc(&swparams)
	defer C.free(unsafe.Pointer(swparams))

	if err := C.snd_pcm_sw_params_current(out.pcmHandle, swparams); err < 0 {
		return out.alsaError("snd_pcm_sw_params_current", err)
	}

	if err := C.snd_pcm_sw_params_set_start_threshold(out.pcmHandle, swparams, C.ulong(out.bufferSize-out.periodSize)); err < 0 {
		return out.alsaError("snd_pcm_sw_params_set_start_threshold", err)
	}

	if err := C.snd_pcm_sw_params_set_avail_min(out.pcmHandle, swparams, C.ulong(out.periodSize)); err < 0 {
		return out.alsaError("snd_pcm_sw_params_set_avail_min", err)
	}

	if err := C.snd_pcm_sw_params(out.pcmHandle, swparams); err < 0 {
		return out.alsaError("snd_pcm_sw_params", err)
	}

	return nil
}

func (out *output) logParams(params *C.snd_pcm_hw_params_t) error {
	var dir C.int

	var rate C.uint
	if err := C.snd_pcm_hw_params_get_rate(params, &rate, &dir); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_rate", err)
	}

	var periodTime C.uint
	if err := C.snd_pcm_hw_params_get_period_time(params, &periodTime, &dir); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_period_time", err)
	}

	var frames C.snd_pcm_uframes_t
	if err := C.snd_pcm_hw_params_get_period_size(params, &frames, &dir); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_period_size", err)
	}

	var bufferTime C.uint
	if err := C.snd_pcm_hw_params_get_buffer_time(params, &bufferTime, &dir); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_buffer_time", err)
	}

	var bufferSize C.ulong
	if err := C.snd_pcm_hw_params_get_buffer_size(params, &bufferSize); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_buffer_size", err)
	}

	var periods C.uint
	if err := C.snd_pcm_hw_params_get_periods(params, &periods, &dir); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_periods", err)
	}

	log.Debugf("alsa driver configured, rate = %d bps, period time = %d us, period size = %d frames, buffer time = %d us, buffer size = %d frames, periods per buffer = %d frames",
		rate, periodTime, frames, bufferTime, bufferSize, periods)

	return nil
}

func (out *output) readLoop() error {
	for {
		floats := make([]float32, out.channels*out.periodSize)
		n, err := out.reader.Read(floats)
		if n > 0 {
			floats = floats[:n]
			if err := out.samples.PutWait(floats); errors.Is(err, ErrBufferClosed) {
				return nil
			} else if err != nil {
				_ = out.samples.Close()
				return err
			}
		}

		if errors.Is(err, io.EOF) {
			_ = out.samples.Close()
			return nil
		} else if err != nil {
			_ = out.samples.Close()
			return err
		}
	}
}

func (out *output) writeLoop() error {
	for {
		floats, err := out.samples.GetWait()
		if errors.Is(err, ErrBufferClosed) {
			out.cond.L.Lock()
			C.snd_pcm_drain(out.pcmHandle)
			out.cond.L.Unlock()

			return nil
		} else if err != nil {
			_ = out.samples.Close()
			return err
		}

		for i := 0; i < len(floats); i++ {
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

		if nn := C.snd_pcm_writei(out.pcmHandle, unsafe.Pointer(&floats[0]), C.snd_pcm_uframes_t(len(floats)/out.channels)); nn < 0 {
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
			if err := out.setupPcm(); err != nil {
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

	out.samples.Clear()

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
