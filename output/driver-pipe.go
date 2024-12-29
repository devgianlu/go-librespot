package output

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
)

type pipeOutput struct {
	reader librespot.Float32Reader
	file   *os.File

	lock sync.Mutex
	cond *sync.Cond

	externalVolume bool

	volume float32
	paused bool
	closed bool

	volumeUpdate chan float32
	err          chan error
}

func newPipeOutput(opts *NewOutputOptions) (out *pipeOutput, err error) {
	out = &pipeOutput{
		reader:         opts.Reader,
		volume:         opts.InitialVolume,
		err:            make(chan error, 2),
		externalVolume: opts.ExternalVolume,
		volumeUpdate:   opts.VolumeUpdate,
	}

	out.cond = sync.NewCond(&out.lock)

	out.file, err = os.OpenFile(opts.OutputPipe, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open fifo: %w", err)
	}

	go out.outputLoop()

	return out, nil
}

func (out *pipeOutput) outputLoop() {
	floats := make([]float32, 16*1024)

	for {
		out.lock.Lock()

		for out.paused && !out.closed {
			out.cond.Wait()
		}

		if out.closed {
			out.lock.Unlock()
			break
		}

		n, err := out.reader.Read(floats)

		// Apply volume.
		if !out.externalVolume {
			// Map volume (in percent) to what is perceived as linear by
			// humans. This is the same as math.Pow(out.volume, 2) but simpler.
			volume := out.volume * out.volume

			for i := 0; i < n; i++ {
				floats[i] *= volume
			}
		}

		if n > 0 {
			// Little trickery to write the floats to the file without copying the buffer.
			_, err := out.file.Write(unsafe.Slice((*byte)(unsafe.Pointer(&floats[0])), n*4))
			if err != nil {
				out.err <- err
				out.closed = true
				out.lock.Unlock()
				break
			}
		}

		if errors.Is(err, io.EOF) {
			// Reached EOF, move to a "paused" state.
			out.paused = true
		} else if err != nil {
			// Got some other error. Close the output and report the error.
			out.err <- err
			out.closed = true
			out.lock.Unlock()
			break
		}

		out.lock.Unlock()
	}

	_ = out.Close()
}

func (out *pipeOutput) Pause() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed {
		return nil
	}

	out.paused = true
	out.cond.Signal()
	return nil
}

func (out *pipeOutput) Resume() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed {
		return nil
	}

	out.paused = false
	out.cond.Signal()
	return nil
}

func (out *pipeOutput) Drop() error {
	return nil
}

func (out *pipeOutput) DelayMs() (int64, error) {
	return 0, nil
}

func (out *pipeOutput) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}

	out.volume = vol
	sendVolumeUpdate(out.volumeUpdate, vol)
}

func (out *pipeOutput) Error() <-chan error {
	// No need to lock here (out.err is only set in newOutput).
	return out.err
}

func (out *pipeOutput) Close() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed {
		return nil
	}

	_ = out.file.Close()

	out.closed = true
	out.cond.Signal()

	return nil
}
