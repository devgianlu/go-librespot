//go:build windows

package output

import "C"
import (
	"errors"
	"fmt"
	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"golang.org/x/sys/windows"
	"io"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func isOleError(err error, code uintptr) bool {
	var oleError *ole.OleError
	if errors.As(err, &oleError) {
		return oleError.Code() == code
	}
	return false
}

func isFalseError(err error) bool {
	return errors.Is(err, syscall.Errno(windows.S_FALSE))
}

type comThread struct {
	fn   chan func()
	stop chan struct{}
}

func newCOMThread() (*comThread, error) {
	funcCh, errCh, stopCh := make(chan func()), make(chan error), make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !isFalseError(err) {
			errCh <- err
			return
		}

		errCh <- nil

	loop:
		for {
			select {
			case <-stopCh:
				break loop
			case fn := <-funcCh:
				fn()
			}
		}

		close(funcCh)
		windows.CoUninitialize()
	}()

	if err := <-errCh; err != nil {
		return nil, err
	}

	return &comThread{funcCh, stopCh}, nil
}

func (com *comThread) Stop() {
	com.stop <- struct{}{}
}

func (com *comThread) Run(f func() error) error {
	ch := make(chan error)
	com.fn <- func() { ch <- f() }
	return <-ch
}

type output struct {
	channels   int
	sampleRate int
	device     string
	reader     librespot.Float32Reader
	done       chan error

	cond *sync.Cond

	com           *comThread
	ev            windows.Handle
	enumerator    *wca.IMMDeviceEnumerator
	client        *wca.IAudioClient2
	renderClient  *wca.IAudioRenderClient
	bufferFrames  uint32
	currentDevice string

	volume float32
	paused bool
	closed bool
	eof    bool
}

var (
	errDeviceSwitched = errors.New("oto: device switched")
)

func newOutput(reader librespot.Float32Reader, sampleRate int, channels int, device string, initiallyPaused bool) (_ *output, err error) {
	out := &output{
		reader:     reader,
		channels:   channels,
		sampleRate: sampleRate,
		device:     device, // FIXME
		volume:     1,
		cond:       sync.NewCond(&sync.Mutex{}),
		done:       make(chan error, 1),
	}

	if out.com, err = newCOMThread(); err != nil {
		return nil, fmt.Errorf("failed creating COM thread: %w", err)
	}

	if out.ev, err = windows.CreateEventEx(nil, nil, 0, windows.EVENT_ALL_ACCESS); err != nil {
		return nil, fmt.Errorf("failed creating windows event: %w", err)
	}

	if err = out.openAndSetup(); err != nil {
		_ = windows.CloseHandle(out.ev)
		return nil, err
	}

	if initiallyPaused {
		_ = out.Pause()
	} else {
		_ = out.Resume()
	}

	go func() {
		err := out.loop()
		_ = out.Close()

		if err != nil {
			if errors.Is(err, errDeviceSwitched) ||
				isOleError(err, wca.AUDCLNT_E_DEVICE_INVALIDATED) ||
				isOleError(err, 0x88890026 /* AUDCLNT_E_RESOURCES_INVALIDATED */) {
				panic("maybe restart?") // FIXME
				return
			}

			out.done <- err
		} else {
			out.done <- nil
		}
	}()

	return out, nil
}

type CorrectAudioClientProperties struct {
	CbSize                uint32
	BIsOffload            int32
	AUDIO_STREAM_CATEGORY uint32
	AUDCLNT_STREAMOPTIONS uint32
}

func (out *output) openAndSetup() error {
	return out.com.Run(func() error {
		if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &out.enumerator); err != nil {
			return fmt.Errorf("failed creating IMMDeviceEnumerator instance: %w", err)
		}

		// TODO: allow selecting device id from out.device property
		// TODO: use RegisterEndpointNotificationCallback to determine default audio endpoint change

		var device *wca.IMMDevice
		if err := out.enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &device); err != nil {
			return fmt.Errorf("failed getting default audio endpoint: %w", err)
		}

		defer device.Release()

		if err := device.GetId(&out.currentDevice); err != nil {
			return fmt.Errorf("failed getting IMMDevice id: %w", err)
		}

		log.Debugf("using IMMDevice with id: %s", out.currentDevice)

		if err := device.Activate(wca.IID_IAudioClient2, wca.CLSCTX_ALL, nil, &out.client); err != nil {
			return fmt.Errorf("failed activating IAudioClient2: %w", err)
		}

		// FIXME: remove this ugly hack when https://github.com/moutend/go-wca/pull/17 is merged
		if err := out.client.SetClientProperties((*wca.AudioClientProperties)(unsafe.Pointer(&CorrectAudioClientProperties{
			CbSize:                uint32(unsafe.Sizeof(CorrectAudioClientProperties{})),
			BIsOffload:            0,
			AUDIO_STREAM_CATEGORY: wca.AudioCategory_BackgroundCapableMedia,
		}))); err != nil {
			return fmt.Errorf("failed setting IAudioClient2 properties: %w", err)
		}

		const bitsPerSample = 32
		nBlockAlign := out.channels * bitsPerSample / 8

		pwfx := &wca.WAVEFORMATEX{
			WFormatTag:      0x3, /* WAVE_FORMAT_IEEE_FLOAT */
			NChannels:       uint16(out.channels),
			NSamplesPerSec:  uint32(out.sampleRate),
			NAvgBytesPerSec: uint32(out.sampleRate * nBlockAlign),
			NBlockAlign:     uint16(nBlockAlign),
			WBitsPerSample:  bitsPerSample,
			CbSize:          0,
		}

		// FIXME: ugly hack because library is broken
		init := func(shareMode uint32, streamFlags uint32, hnsBufferDuration wca.REFERENCE_TIME, hnsPeriodicity wca.REFERENCE_TIME, pFormat *wca.WAVEFORMATEX, audioSessionGuid *windows.GUID) error {
			var r uintptr
			if unsafe.Sizeof(uintptr(0)) == 8 {
				// 64bits
				r, _, _ = syscall.SyscallN(out.client.VTable().Initialize, uintptr(unsafe.Pointer(out.client)),
					uintptr(shareMode), uintptr(streamFlags), uintptr(hnsBufferDuration),
					uintptr(hnsPeriodicity), uintptr(unsafe.Pointer(pFormat)), uintptr(unsafe.Pointer(audioSessionGuid)))
			} else {
				// 32bits
				r, _, _ = syscall.SyscallN(out.client.VTable().Initialize, uintptr(unsafe.Pointer(out.client)),
					uintptr(shareMode), uintptr(streamFlags), uintptr(hnsBufferDuration),
					uintptr(hnsBufferDuration>>32), uintptr(hnsPeriodicity), uintptr(hnsPeriodicity>>32),
					uintptr(unsafe.Pointer(pFormat)), uintptr(unsafe.Pointer(audioSessionGuid)))
			}
			runtime.KeepAlive(pFormat)
			runtime.KeepAlive(audioSessionGuid)
			if r != 0 {
				return ole.NewError(r)
			}

			return nil
		}

		if err := init(
			wca.AUDCLNT_SHAREMODE_SHARED,
			wca.AUDCLNT_STREAMFLAGS_EVENTCALLBACK|wca.AUDCLNT_STREAMFLAGS_NOPERSIST|wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM,
			wca.REFERENCE_TIME(50*time.Millisecond/100),
			0, pwfx, nil,
		); err != nil {
			return fmt.Errorf("failed initializing IAudioClient2: %w", err)
		}

		if err := out.client.GetBufferSize(&out.bufferFrames); err != nil {
			return fmt.Errorf("failed getting buffer size from IAudioClient2: %w", err)
		}

		if err := out.client.GetService(wca.IID_IAudioRenderClient, &out.renderClient); err != nil {
			return fmt.Errorf("failed getting IAudioRenderClient service from IAudioClient2: %w", err)
		}

		if err := out.client.SetEventHandle(uintptr(out.ev)); err != nil {
			return fmt.Errorf("failed setting event handle on IAudioClient2: %w", err)
		}

		return nil
	})
}

func (out *output) loop() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !isFalseError(err) {
		return fmt.Errorf("failed initializing COM thread: %w", err)
	}

	defer windows.CoUninitialize()

	floats := make([]float32, int(out.bufferFrames)*out.channels)

	for {
		out.cond.L.Lock()
		for !(!out.paused || out.closed) {
			out.cond.Wait()
		}

		if out.closed {
			out.cond.L.Unlock()
			return nil
		}

		if evt, err := windows.WaitForSingleObject(out.ev, windows.INFINITE); err != nil {
			out.cond.L.Unlock()
			return fmt.Errorf("failed waiting for single object: %w", err)
		} else if evt != windows.WAIT_OBJECT_0 {
			out.cond.L.Unlock()
			return fmt.Errorf("unexpected event while waiting for single object: %v", evt)
		}

		var currentPadding uint32
		if err := out.client.GetCurrentPadding(&currentPadding); err != nil {
			out.cond.L.Unlock()
			return fmt.Errorf("failed getting current padding from IAudioClient2: %w", err)
		}

		// we can unlock here since we are going to touch only the reader,
		// additionally it usually takes a while for it to finish.
		out.cond.L.Unlock()

		frames := out.bufferFrames - currentPadding
		if frames <= 0 {
			continue
		}

		n, err := out.reader.Read(floats[:int(frames)*out.channels])
		if errors.Is(err, io.EOF) {
			out.eof = true

			// FIXME: this thing churns the data too fast, buffer is way too big
			println("EOF")

			// FIXME: consider waiting for audio to drain before exiting
			//		  see GetDevicePeriod(NULL, &hnsRequestedDuration) and
			//        https://learn.microsoft.com/en-us/windows/win32/coreaudio/exclusive-mode-streams or
			//        https://learn.microsoft.com/en-us/windows/win32/coreaudio/rendering-a-stream
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

		// lock again because we need to touch the render client
		out.cond.L.Lock()
		if out.closed {
			return nil
		}

		var buf *byte
		if err := out.renderClient.GetBuffer(frames, &buf); err != nil {
			out.cond.L.Unlock()
			return fmt.Errorf("failed getting buffer from IAudioRenderClient: %w", err)
		}

		// we are forced to use copy here and cannot read directly into this buffer, from the docs:
		//   Clients should avoid excessive delays between the GetBuffer call that
		//   acquires a buffer and the ReleaseBuffer call that releases the buffer.
		copy(unsafe.Slice((*float32)(unsafe.Pointer(buf)), int(frames)*out.channels), floats[:n])

		if err := out.renderClient.ReleaseBuffer(uint32(n/out.channels), 0); err != nil {
			out.cond.L.Unlock()
			return fmt.Errorf("failed releasing buffer to IAudioRenderClient: %w", err)
		}

		out.cond.L.Unlock()
	}
}

func (out *output) Pause() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if err := out.client.Stop(); err != nil && !isFalseError(err) {
		return err
	}

	out.paused = true
	return nil
}

func (out *output) Resume() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if err := out.client.Start(); err != nil && !isOleError(err, wca.AUDCLNT_E_NOT_STOPPED) {
		return err
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

	out.com.Stop()
	out.renderClient.Release()
	out.client.Release()
	out.enumerator.Release()
	_ = windows.CloseHandle(out.ev)

	out.closed = true
	out.cond.Signal()

	return nil
}
