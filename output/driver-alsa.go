//go:build !android && !darwin && !js && !windows && !nintendosdk

package output

// #cgo pkg-config: alsa
//
// #include <alsa/asoundlib.h>
//
import "C"
import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
)

const (
	defaultBufferTimeMicro = 500_000
	defaultNumPeriods      = 4
)

type alsaOutput struct {
	log librespot.Logger

	channels   int
	sampleRate int
	device     string
	reader     librespot.Float32Reader

	lock sync.Mutex

	pcmHandle   *C.snd_pcm_t // nil when pcmHandle is closed
	bufferTime  int
	periodCount int
	periodSize  int
	bufferSize  int
	pcmFormat   C.snd_pcm_format_t

	externalVolume bool

	mixer   string
	control string

	mixerEnabled    bool
	mixerHandle     *C.snd_mixer_t
	mixerElemHandle *C.snd_mixer_elem_t
	mixerMinVolume  C.long
	mixerMaxVolume  C.long

	volume float32
	closed bool

	volumeUpdate chan float32
	err          chan error
}

func newAlsaOutput(opts *NewOutputOptions) (*alsaOutput, error) {
	out := &alsaOutput{
		log:            opts.Log,
		reader:         opts.Reader,
		channels:       opts.ChannelCount,
		sampleRate:     opts.SampleRate,
		device:         opts.Device,
		mixer:          opts.Mixer,
		control:        opts.Control,
		volume:         opts.InitialVolume,
		err:            make(chan error, 2),
		externalVolume: opts.ExternalVolume,
		volumeUpdate:   opts.VolumeUpdate,
	}

	if opts.BufferTimeMicro == 0 {
		out.bufferTime = defaultBufferTimeMicro
	} else {
		out.bufferTime = opts.BufferTimeMicro
	}

	if opts.PeriodCount == 0 {
		out.periodCount = defaultNumPeriods
	} else {
		out.periodCount = opts.PeriodCount
	}

	if err := out.setupMixer(); err != nil {
		if uintptr(unsafe.Pointer(out.mixerHandle)) != 0 {
			C.snd_mixer_close(out.mixerHandle)
		}

		out.mixerEnabled = false
		out.log.WithError(err).Warnf("failed setting up output device mixer")
	}

	return out, nil
}

func (out *alsaOutput) alsaError(name string, err C.int) error {
	return fmt.Errorf("ALSA error at %s: %s", name, C.GoString(C.snd_strerror(err)))
}

func (out *alsaOutput) isFormatSupported(hwparams *C.snd_pcm_hw_params_t, format C.snd_pcm_format_t) bool {
	errCode := C.snd_pcm_hw_params_test_format(out.pcmHandle, hwparams, format)
	if errCode < 0 {
		return false
	}

	return true
}

func (out *alsaOutput) setupPcm() error {
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

	var formatFound bool
	for _, format := range []C.snd_pcm_format_t{
		C.SND_PCM_FORMAT_FLOAT_LE, // 32-bit floating point, little-endian
		C.SND_PCM_FORMAT_S32_LE,   // 32-bit signed integer, little-endian
		C.SND_PCM_FORMAT_S16_LE,   // 16-bit signed integer, little-endian
	} {
		if !out.isFormatSupported(hwparams, format) {
			continue
		}

		if err := C.snd_pcm_hw_params_set_format(out.pcmHandle, hwparams, format); err < 0 {
			return out.alsaError("snd_pcm_hw_params_set_format", err)
		}
		formatFound = true
	}

	if !formatFound {
		return fmt.Errorf("could not find a supported PCM format")
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

	bufferTime := C.uint(out.bufferTime)
	if err := C.snd_pcm_hw_params_set_buffer_time_near(out.pcmHandle, hwparams, &bufferTime, nil); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_buffer_time_near", err)
	}

	// Request a period size that's approximately bufferSize/4.
	// By default, it might use a really short buffer size like 220 which can
	// lead to crackling.
	var bufferSize C.snd_pcm_uframes_t
	if err := C.snd_pcm_hw_params_get_buffer_size(hwparams, &bufferSize); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_buffer_size", err)
	}
	var periodSize C.snd_pcm_uframes_t = C.snd_pcm_uframes_t(bufferSize) / C.snd_pcm_uframes_t(out.periodCount)
	if err := C.snd_pcm_hw_params_set_period_size_near(out.pcmHandle, hwparams, &periodSize, nil); err < 0 {
		return out.alsaError("snd_pcm_hw_params_set_period_size_near", err)
	}

	if err := C.snd_pcm_hw_params(out.pcmHandle, hwparams); err < 0 {
		return out.alsaError("snd_pcm_hw_params", err)
	}

	_ = out.logParams(hwparams)

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

	var pcmFormat C.snd_pcm_format_t
	if err := C.snd_pcm_hw_params_get_format(hwparams, &pcmFormat); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_format", err)
	} else {
		out.pcmFormat = pcmFormat
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

	// Move samples from the reader out to the ALSA device.
	// This loop continues until the PCM handle is closed and set to nil
	// (Pause() or Close()) or there is an error.
	pcmHandle := out.pcmHandle
	go out.outputLoop(pcmHandle)

	return nil
}

func pcmFormatName(format C.snd_pcm_format_t) string {
	switch format {
	case C.SND_PCM_FORMAT_S8:
		return "S8"
	case C.SND_PCM_FORMAT_U8:
		return "U8"
	case C.SND_PCM_FORMAT_S16_LE:
		return "S16_LE"
	case C.SND_PCM_FORMAT_S16_BE:
		return "S16_BE"
	case C.SND_PCM_FORMAT_U16_LE:
		return "U16_LE"
	case C.SND_PCM_FORMAT_U16_BE:
		return "U16_BE"
	case C.SND_PCM_FORMAT_S24_LE:
		return "S24_LE"
	case C.SND_PCM_FORMAT_S24_BE:
		return "S24_BE"
	case C.SND_PCM_FORMAT_U24_LE:
		return "U24_LE"
	case C.SND_PCM_FORMAT_U24_BE:
		return "U24_BE"
	case C.SND_PCM_FORMAT_S32_LE:
		return "S32_LE"
	case C.SND_PCM_FORMAT_S32_BE:
		return "S32_BE"
	case C.SND_PCM_FORMAT_U32_LE:
		return "U32_LE"
	case C.SND_PCM_FORMAT_U32_BE:
		return "U32_BE"
	case C.SND_PCM_FORMAT_FLOAT_LE:
		return "FLOAT_LE"
	case C.SND_PCM_FORMAT_FLOAT_BE:
		return "FLOAT_BE"
	default:
		return "unknown"
	}
}

func (out *alsaOutput) logParams(params *C.snd_pcm_hw_params_t) error {
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

	var format C.snd_pcm_format_t
	if err := C.snd_pcm_hw_params_get_format(params, &format); err < 0 {
		return out.alsaError("snd_pcm_hw_params_get_format", err)
	}

	out.log.Debugf("alsa driver configured, rate = %d bps, period time = %d us, period size = %d frames, buffer time = %d us, buffer size = %d frames, periods per buffer = %d frames, PCM format = %s",
		rate, periodTime, frames, bufferTime, bufferSize, periods, pcmFormatName(format))

	return nil
}

func floatLeToS16Le(floats []float32) []C.int16_t {
	shorts := make([]C.int16_t, len(floats))
	for i, f := range floats {
		// Clip the float to the range [-1.0, 1.0]
		if f < -1.0 {
			f = -1.0
		}
		if f > 1.0 {
			f = 1.0
		}

		// Convert to int16 (short) in the range [-32768, 32767]
		shorts[i] = C.int16_t(f * 32767)
	}
	return shorts
}

func floatLeToS32Le(floats []float32) []C.int32_t {
	ints := make([]C.int32_t, len(floats))
	for i, f := range floats {
		// Clip the float to the range [-1.0, 1.0]
		if f < -1.0 {
			f = -1.0
		}
		if f > 1.0 {
			f = 1.0
		}

		// Convert to int32 in the range [-2147483648, 2147483647]
		ints[i] = C.int32_t(f * 2147483647)
	}
	return ints
}

func (out *alsaOutput) outputLoop(pcmHandle *C.snd_pcm_t) {
	floats := make([]float32, out.channels*out.periodSize)

	for {
		// Calculate how long we should wait until there's enough space in the
		// ALSA buffer for writing.
		// Note: we'll wait until periodSize*1.125 frames can be written,
		// because in that case snd_pcm_writei won't be delayed (for some
		// reason, it will be delayed when waiting for exactly periodSize -
		// apparently not enough frames are ready to write even when waiting for
		// the appropriate amount of time).
		out.lock.Lock()
		if pcmHandle != out.pcmHandle {
			// Either out.pcmHandle is nil, or it is a new pcm handle entirely
			// (with a very fast pause+resume). In both cases, the loop should
			// be stopped.
			out.lock.Unlock()
			return
		}
		availableFrames := int(C.snd_pcm_avail(pcmHandle))
		waitForFrames := (out.periodSize + out.periodSize/8) - availableFrames
		waitTime := time.Duration(waitForFrames) * time.Second / time.Duration(out.sampleRate)
		out.lock.Unlock()

		// Wait until enough frames are available, with the output unlocked so
		// that things like pause can still happen in the meantime.
		time.Sleep(waitTime)

		// Make sure we're either ready to play, or we need to stop this loop
		// (because the ALSA output is paused/closed).
		out.lock.Lock()
		if pcmHandle != out.pcmHandle {
			out.lock.Unlock()
			return
		}

		// Read audio data. This can take a few milliseconds because it needs to
		// decode the audio data.
		n, err := out.reader.Read(floats)

		// Apply volume.
		if !out.mixerEnabled && !out.externalVolume {
			// Map volume (in percent) to what is perceived as linear by
			// humans. This is the same as math.Pow(out.volume, 2) but simpler.
			volume := out.volume * out.volume

			for i := 0; i < n; i++ {
				floats[i] *= volume
			}
		}

		// Write audio data to the device. This just copies a buffer, so should
		// be very fast. It might be delayed a bit however if the sleep above
		// didn't sleep long enough to wait for room in the buffer.
		if n > 0 {
			var data unsafe.Pointer
			switch out.pcmFormat {
			case C.SND_PCM_FORMAT_FLOAT_LE:
				data = unsafe.Pointer(&floats[0])
			case C.SND_PCM_FORMAT_S32_LE:
				ints := floatLeToS32Le(floats)
				data = unsafe.Pointer(&ints[0])
			case C.SND_PCM_FORMAT_S16_LE:
				shorts := floatLeToS16Le(floats)
				data = unsafe.Pointer(&shorts[0])
			default:
				out.err <- fmt.Errorf("unsupported PCM format: %s", pcmFormatName(out.pcmFormat))
				out.closed = true
				out.lock.Unlock()
				return
			}

			if nn := C.snd_pcm_writei(pcmHandle, data, C.snd_pcm_uframes_t(n/out.channels)); nn < 0 {
				// Got an error, so must recover (even for an underrun).
				errCode := C.snd_pcm_recover(pcmHandle, C.int(nn), 1)
				if errCode < 0 {
					// Failed to recover from this error. Close the output and
					// report the error.
					out.err <- out.alsaError("snd_pcm_recover", C.int(errCode))
					out.closed = true
					out.lock.Unlock()
					return
				}
			}
		}

		if errors.Is(err, io.EOF) {
			// Reached EOF, move to a "paused" state.
			out.pcmHandle = nil
			if errCode := C.snd_pcm_close(pcmHandle); errCode < 0 {
				out.err <- out.alsaError("snd_pcm_close", errCode)
				out.lock.Unlock()
			}
			out.lock.Unlock()
			return
		} else if err != nil {
			// Got some other error. Close the output and report the error.
			out.err <- err
			out.closed = true
			out.lock.Unlock()
			return
		}

		out.lock.Unlock()
	}
}

func (out *alsaOutput) Pause() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed || out.pcmHandle == nil {
		return nil
	}

	if err := C.snd_pcm_close(out.pcmHandle); err < 0 {
		return out.alsaError("snd_pcm_close", err)
	}
	out.pcmHandle = nil

	return nil
}

func (out *alsaOutput) Resume() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed || out.pcmHandle != nil {
		return nil
	}

	if err := out.setupPcm(); err != nil {
		return err
	}

	return nil
}

func (out *alsaOutput) Drop() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed || out.pcmHandle == nil {
		return nil
	}

	if err := C.snd_pcm_drop(out.pcmHandle); err < 0 {
		return out.alsaError("snd_pcm_drop", err)
	}

	// Since we are not actually stopping the stream, prepare it again.
	if err := C.snd_pcm_prepare(out.pcmHandle); err < 0 {
		return out.alsaError("snd_pcm_prepare", err)
	}

	return nil
}

func (out *alsaOutput) DelayMs() (int64, error) {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed || out.pcmHandle == nil {
		return 0, nil
	}

	var frames C.snd_pcm_sframes_t
	if err := C.snd_pcm_delay(out.pcmHandle, &frames); err < 0 {
		return 0, out.alsaError("snd_pcm_delay", err)
	}

	return int64(frames) * 1000 / int64(out.sampleRate), nil
}

func (out *alsaOutput) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	} else if vol == out.volume {
		// No need to update the volume if it didn't change.
		return
	}

	out.volume = vol
	sendVolumeUpdate(out.volumeUpdate, vol)

	if out.mixerEnabled && !out.externalVolume {
		placeholder := C.float(-1)
		C.snd_mixer_elem_set_callback_private(out.mixerElemHandle, unsafe.Pointer(&placeholder))

		mixerVolume := vol*(float32(out.mixerMaxVolume-out.mixerMinVolume)) + float32(out.mixerMinVolume)
		out.log.Debugf("updating alsa mixer volume to %d/%d\n", C.long(mixerVolume), out.mixerMaxVolume)
		if err := C.snd_mixer_selem_set_playback_volume_all(out.mixerElemHandle, C.long(mixerVolume)); err != 0 {
			out.log.WithError(out.alsaError("snd_mixer_selem_set_playback_volume_all", err)).Warnf("failed setting output device mixer volume")
		}
	}
}

func (out *alsaOutput) Error() <-chan error {
	// No need to lock here (out.err is only set in newOutput).
	return out.err
}

func (out *alsaOutput) Close() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed {
		return nil
	}

	if out.pcmHandle != nil {
		if err := C.snd_pcm_close(out.pcmHandle); err < 0 {
			return out.alsaError("snd_pcm_close", err)
		}
		out.pcmHandle = nil
	}

	if out.mixerEnabled {
		if err := C.snd_mixer_close(out.mixerHandle); err < 0 {
			return out.alsaError("snd_mixer_close", err)
		}
	}

	out.closed = true

	return nil
}
