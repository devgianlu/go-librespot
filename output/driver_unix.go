//go:build !android && !darwin && !js && !windows && !nintendosdk

package output

// #cgo pkg-config: alsa
//
// #include <alsa/asoundlib.h>
// extern int alsaMixerCallback(snd_mixer_elem_t*, unsigned int);
//
import "C"
import (
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
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

	externalVolume bool

	mixer   string
	control string

	mixerEnabled    bool
	mixerHandle     *C.snd_mixer_t
	mixerElemHandle *C.snd_mixer_elem_t
	mixerMinVolume  C.long
	mixerMaxVolume  C.long

	volume   float32
	paused   bool
	closed   bool
	released bool

	externalVolumeUpdate RingBuffer[float32]
	err                  chan error
}

func newOutput(reader librespot.Float32Reader, sampleRate int, channels int, device string, mixer string, control string, initialVolume float32, externalVolume bool, externalVolumeUpdate RingBuffer[float32]) (*output, error) {
	out := &output{
		reader:               reader,
		channels:             channels,
		sampleRate:           sampleRate,
		device:               device,
		mixer:                mixer,
		control:              control,
		volume:               initialVolume,
		err:                  make(chan error, 1),
		cond:                 sync.NewCond(&sync.Mutex{}),
		externalVolume:       externalVolume,
		externalVolumeUpdate: externalVolumeUpdate,
	}

	if err := out.openAndSetup(); err != nil {
		return nil, err
	}

	if err := out.openAndSetupMixer(); err != nil {
		if uintptr(unsafe.Pointer(out.mixerHandle)) != 0 {
			C.snd_mixer_close(out.mixerHandle)
		}

		out.mixerEnabled = false
		log.WithError(err).Warnf("failed setting up output device mixer")
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

func (out *output) openAndSetupMixer() error {
	if len(out.mixer) == 0 {
		out.mixerEnabled = false
		return nil
	}

	if err := C.snd_mixer_open(&out.mixerHandle, 0); err < 0 {
		return out.alsaError("snd_mixer_open", err)
	}

	cmixer := C.CString(out.mixer)
	defer C.free(unsafe.Pointer(cmixer))
	if err := C.snd_mixer_attach(out.mixerHandle, cmixer); err < 0 {
		return out.alsaError("snd_mixer_attach", err)
	}

	if err := C.snd_mixer_selem_register(out.mixerHandle, nil, nil); err < 0 {
		return out.alsaError("snd_mixer_selem_register", err)
	}

	if err := C.snd_mixer_load(out.mixerHandle); err < 0 {
		return out.alsaError("snd_mixer_load", err)
	}

	var sid *C.snd_mixer_selem_id_t
	if err := C.snd_mixer_selem_id_malloc(&sid); err < 0 {
		return out.alsaError("snd_mixer_selem_id_malloc", err)
	}
	defer C.free(unsafe.Pointer(sid))

	C.snd_mixer_selem_id_set_index(sid, 0)
	C.snd_mixer_selem_id_set_name(sid, C.CString(out.control))

	if out.mixerElemHandle = C.snd_mixer_find_selem(out.mixerHandle, sid); uintptr(unsafe.Pointer(out.mixerElemHandle)) == 0 {
		return fmt.Errorf("mixer simple element not found")
	}

	if err := C.snd_mixer_selem_get_playback_volume_range(out.mixerElemHandle, &out.mixerMinVolume, &out.mixerMaxVolume); err < 0 {
		return out.alsaError("snd_mixer_selem_get_playback_volume_range", err)
	}

	// get current volume from the mixer, and set the spotify volume accordingly
	var volume C.long
	C.snd_mixer_selem_get_playback_volume(out.mixerElemHandle, C.SND_MIXER_SCHN_MONO, &volume)
	out.volume = float32(volume-out.mixerMinVolume) / float32(out.mixerMaxVolume-out.mixerMinVolume)

	out.externalVolumeUpdate.Put(out.volume)

	// set callback and initialize private
	var cb C.snd_mixer_elem_callback_t = (C.snd_mixer_elem_callback_t)(C.alsaMixerCallback)
	C.snd_mixer_elem_set_callback(out.mixerElemHandle, cb)
	C.snd_mixer_elem_set_callback_private(out.mixerElemHandle, unsafe.Pointer(&out.volume))

	go out.waitForMixerEvents()

	out.mixerEnabled = true
	return nil
}

func (out *output) waitForMixerEvents() {
	for !out.closed {
		var res = C.snd_mixer_wait(out.mixerHandle, -1)
		if out.closed {
			// if we reach here, the playing context has probably changed
			break
		}
		if res >= 0 {
			res = C.snd_mixer_handle_events(out.mixerHandle)
			if res <= 0 {
				errStrPtr := C.snd_strerror(res)
				log.Warnf("error while handling alsa mixer events. (%s)\n", string(C.GoString(errStrPtr)))

				// no need to free the errStrPtr, because it doesn't point into heap
				continue
			}

			var priv = float32(*(*C.float)(C.snd_mixer_elem_get_callback_private(out.mixerElemHandle)))
			if priv < 0 {
				// volume update came from spotify, so no need to tell spotify about it
				// reset the private, but discard the event
				C.snd_mixer_elem_set_callback_private(out.mixerElemHandle, unsafe.Pointer(&out.volume))

				continue
			}
			if priv == out.volume {
				log.Debugf("skipping alsa mixer event, volume already updated: %.2f\n", priv)
				continue
			}

			out.externalVolumeUpdate.Put(priv)
		} else {
			errStrPtr := C.snd_strerror(res)
			log.Warnf("error while waiting for alsa mixer events. (%s)\n", string(C.GoString(errStrPtr)))
		}
	}
}

/*
The mixer callback private is used to pass the detected volume from c back to go code.

A private value between zero and one (inclusive) means, that the volume changed to that percentage of the maximum volume.

A private value less than zero means, that the volume update was initiated by spotify instead of the alsa mixer.
*/
//export alsaMixerCallback
func alsaMixerCallback(elem *C.snd_mixer_elem_t, _ C.uint) C.int {
	if float32(*(*C.float)(C.snd_mixer_elem_get_callback_private(elem))) < 0 {
		// the volume update came from spotify, so there is no need to tell spotify about it
		return 0
	}

	var val C.long
	var minVol C.long
	var maxVol C.long
	C.snd_mixer_selem_get_playback_volume(elem, C.SND_MIXER_SCHN_MONO, &val)
	C.snd_mixer_selem_get_playback_volume_range(elem, &minVol, &maxVol)

	var normalizedVolume = C.float(float32(val-minVol) / float32(maxVol-minVol))
	C.snd_mixer_elem_set_callback_private(elem, unsafe.Pointer(&normalizedVolume))

	return 0
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

		if !out.mixerEnabled && !out.externalVolume {
			for i := 0; i < n; i++ {
				floats[i] *= out.volume
			}
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

	if out.mixerEnabled && !out.externalVolume {
		placeholder := C.float(-1)
		C.snd_mixer_elem_set_callback_private(out.mixerElemHandle, unsafe.Pointer(&placeholder))

		mixerVolume := vol*(float32(out.mixerMaxVolume-out.mixerMinVolume)) + float32(out.mixerMinVolume)
		log.Debugf("updating alsa mixer volume to %.02f\n", mixerVolume)
		if err := C.snd_mixer_selem_set_playback_volume_all(out.mixerElemHandle, C.long(mixerVolume)); err != 0 {
			log.WithError(out.alsaError("snd_mixer_selem_set_playback_volume_all", err)).Warnf("failed setting output device mixer volume")
		}
	}
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

	if out.mixerEnabled {
		if err := C.snd_mixer_close(out.mixerHandle); err < 0 {
			return out.alsaError("snd_mixer_close", err)
		}
	}

	out.closed = true
	out.cond.Signal()

	return nil
}
