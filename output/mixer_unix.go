//go:build !android && !darwin && !js && !windows && !nintendosdk

package output

// #cgo pkg-config: alsa
//
// #include <alsa/asoundlib.h>
// extern int alsaMixerCallback(snd_mixer_elem_t*, unsigned int);
//
import "C"
import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"unsafe"
)

func (out *output) setupMixer() error {
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

// alsaMixerCallback is a private callback used to pass the detected volume from C back to Go code.
// A private value between zero and one (inclusive) means that the volume changed to that percentage of the maximum volume.
// A private value less than zero means that the volume update was initiated by Spotify instead of the ALSA mixer.
//
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
