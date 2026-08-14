//go:build windows

package output

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
	"golang.org/x/sys/windows"
)

const defaultWasapiBufferTimeMicro = 50_000

type wasapiOutput struct {
	log librespot.Logger

	channels   int
	sampleRate int
	reader     librespot.Float32Reader

	lock sync.Mutex
	cond *sync.Cond

	enumerator *immDeviceEnumerator
	client     *iAudioClient
	render     *iAudioRenderClient
	ready      windows.Handle
	bufFrames  uint32

	volume         float32
	externalVolume bool
	volumeUpdate   chan float32

	paused bool
	closed bool

	err  chan error
	done chan struct{}
}

func newWasapiOutput(opts *NewOutputOptions) (Output, error) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return nil, fmt.Errorf("wasapi output requires 64-bit Windows")
	}
	if opts.Reader == nil {
		return nil, fmt.Errorf("wasapi output requires a reader")
	}
	if opts.SampleRate <= 0 || opts.ChannelCount <= 0 {
		return nil, fmt.Errorf("wasapi output requires sample rate and channel count")
	}

	out := &wasapiOutput{
		log:            opts.Log,
		channels:       opts.ChannelCount,
		sampleRate:     opts.SampleRate,
		reader:         opts.Reader,
		volume:         opts.InitialVolume,
		externalVolume: opts.ExternalVolume,
		volumeUpdate:   opts.VolumeUpdate,
		err:            make(chan error, 2),
		done:           make(chan struct{}),
	}
	out.cond = sync.NewCond(&out.lock)

	if err := coInitialize(); err != nil {
		return nil, fmt.Errorf("wasapi: CoInitializeEx: %w", err)
	}

	ev, err := windows.CreateEventEx(nil, nil, 0, windows.EVENT_ALL_ACCESS)
	if err != nil {
		windows.CoUninitialize()
		return nil, fmt.Errorf("wasapi: CreateEventEx: %w", err)
	}
	out.ready = ev

	if err := out.openClient(opts.BufferTimeMicro); err != nil {
		out.release()
		windows.CoUninitialize()
		return nil, err
	}

	go out.loop()

	if err := out.client.Start(); err != nil {
		out.lock.Lock()
		out.closed = true
		out.cond.Signal()
		out.lock.Unlock()
		<-out.done
		out.release()
		windows.CoUninitialize()
		return nil, err
	}

	// The render loop keeps COM initialized. Drop the constructor thread's
	// apartment so Close/Pause can run on any goroutine.
	windows.CoUninitialize()

	if out.log != nil {
		out.log.Info("started wasapi output")
	}
	return out, nil
}

func (out *wasapiOutput) openClient(bufferTimeMicro int) error {
	enum, err := coCreateMMDeviceEnumerator()
	if err != nil {
		return err
	}
	out.enumerator = enum

	dev, err := enum.GetDefaultAudioEndpoint()
	if err != nil {
		return err
	}
	defer dev.Release()

	client, err := dev.ActivateAudioClient()
	if err != nil {
		return err
	}
	out.client = client

	if bufferTimeMicro <= 0 {
		bufferTimeMicro = defaultWasapiBufferTimeMicro
	}
	// REFERENCE_TIME is 100ns. 1µs = 10 of those units.
	if err := client.Initialize(int64(bufferTimeMicro)*10, newFloatFormat(out.sampleRate, out.channels)); err != nil {
		return err
	}

	frames, err := client.GetBufferSize()
	if err != nil {
		return err
	}
	out.bufFrames = frames

	render, err := client.GetRenderClient()
	if err != nil {
		return err
	}
	out.render = render

	return client.SetEventHandle(out.ready)
}

func (out *wasapiOutput) loop() {
	defer close(out.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := coInitialize(); err != nil {
		out.fail(fmt.Errorf("wasapi: render thread CoInitializeEx: %w", err))
		return
	}
	defer windows.CoUninitialize()

	for {
		out.lock.Lock()
		for out.paused && !out.closed {
			out.cond.Wait()
		}
		if out.closed {
			out.lock.Unlock()
			return
		}
		out.lock.Unlock()

		evt, err := windows.WaitForSingleObject(out.ready, 50)
		if err != nil {
			out.fail(fmt.Errorf("wasapi: WaitForSingleObject: %w", err))
			return
		}
		if evt == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		if evt != uint32(windows.WAIT_OBJECT_0) {
			out.fail(fmt.Errorf("wasapi: WaitForSingleObject returned %d", evt))
			return
		}

		if err := out.write(); err != nil {
			out.fail(err)
			return
		}
	}
}

func (out *wasapiOutput) write() error {
	out.lock.Lock()
	if out.closed || out.paused || out.client == nil || out.render == nil {
		out.lock.Unlock()
		return nil
	}
	client := out.client
	render := out.render
	channels := out.channels
	volume := out.volume
	external := out.externalVolume
	out.lock.Unlock()

	padding, err := client.GetCurrentPadding()
	if err != nil {
		return err
	}
	frames := out.bufFrames - padding
	if frames == 0 {
		return nil
	}

	samples := make([]float32, int(frames)*channels)
	n, readErr := out.reader.Read(samples)
	if n > 0 {
		n -= n % channels
	}

	out.lock.Lock()
	defer out.lock.Unlock()
	if out.closed || out.paused || out.render != render {
		return nil
	}

	if n > 0 && !external {
		gain := volume * volume
		for i := 0; i < n; i++ {
			samples[i] *= gain
		}

		dst, err := render.GetBuffer(uint32(n / channels))
		if err != nil {
			return err
		}
		copy(unsafe.Slice((*float32)(unsafe.Pointer(dst)), n), samples[:n])
		if err := render.ReleaseBuffer(uint32(n / channels)); err != nil {
			return err
		}
	}

	if errors.Is(readErr, io.EOF) {
		out.paused = true
		_ = client.Stop()
		return nil
	}
	if readErr != nil {
		return readErr
	}
	return nil
}

func (out *wasapiOutput) fail(err error) {
	out.lock.Lock()
	defer out.lock.Unlock()
	if out.closed {
		return
	}
	out.closed = true
	select {
	case out.err <- err:
	default:
	}
	out.cond.Signal()
}

func (out *wasapiOutput) Pause() error {
	return out.withCOM(func() error {
		out.lock.Lock()
		defer out.lock.Unlock()
		if out.closed || out.paused || out.client == nil {
			return nil
		}
		if err := out.client.Stop(); err != nil {
			return err
		}
		out.paused = true
		out.cond.Signal()
		return nil
	})
}

func (out *wasapiOutput) Resume() error {
	return out.withCOM(func() error {
		out.lock.Lock()
		defer out.lock.Unlock()
		if out.closed || !out.paused || out.client == nil {
			return nil
		}
		if err := out.client.Start(); err != nil {
			return err
		}
		out.paused = false
		out.cond.Signal()
		return nil
	})
}

func (out *wasapiOutput) Drop() error {
	return out.withCOM(func() error {
		out.lock.Lock()
		defer out.lock.Unlock()
		if out.closed || out.client == nil {
			return nil
		}

		playing := !out.paused
		if playing {
			if err := out.client.Stop(); err != nil {
				return err
			}
		}
		if err := out.client.Reset(); err != nil {
			return err
		}
		if playing {
			if err := out.client.Start(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (out *wasapiOutput) DelayMs() (int64, error) {
	var delay int64
	err := out.withCOM(func() error {
		out.lock.Lock()
		defer out.lock.Unlock()
		if out.closed || out.paused || out.client == nil {
			return nil
		}
		padding, err := out.client.GetCurrentPadding()
		if err != nil {
			return err
		}
		delay = int64(padding) * 1000 / int64(out.sampleRate)
		return nil
	})
	return delay, err
}

func (out *wasapiOutput) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}
	out.lock.Lock()
	out.volume = vol
	out.lock.Unlock()
	if out.volumeUpdate != nil {
		sendVolumeUpdate(out.volumeUpdate, vol)
	}
}

func (out *wasapiOutput) Error() <-chan error {
	return out.err
}

func (out *wasapiOutput) Close() error {
	out.lock.Lock()
	if out.closed {
		out.lock.Unlock()
		return nil
	}
	out.closed = true
	if out.client != nil {
		_ = out.client.Stop()
	}
	out.cond.Signal()
	out.lock.Unlock()

	<-out.done
	if err := out.withCOM(func() error {
		out.release()
		return nil
	}); err != nil {
		out.release()
	}
	return nil
}

func (out *wasapiOutput) withCOM(fn func() error) error {
	if err := coInitialize(); err != nil {
		return fmt.Errorf("wasapi: CoInitializeEx: %w", err)
	}
	defer windows.CoUninitialize()
	return fn()
}

func (out *wasapiOutput) release() {
	if out.render != nil {
		out.render.Release()
		out.render = nil
	}
	if out.client != nil {
		out.client.Release()
		out.client = nil
	}
	if out.enumerator != nil {
		out.enumerator.Release()
		out.enumerator = nil
	}
	if out.ready != 0 {
		_ = windows.CloseHandle(out.ready)
		out.ready = 0
	}
}

func coInitialize() error {
	err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.Errno(windows.S_FALSE)) {
		return nil
	}
	return err
}
