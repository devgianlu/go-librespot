package output

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	librespot "github.com/devgianlu/go-librespot"
)

// pipePassthroughOutput implements the pipe_passthrough backend: it writes
// the raw encoded (Ogg/Vorbis) stream from an AudioSourcePassthrough reader
// straight to a named pipe, so a downstream consumer with its own decoder
// (a hardware decoder, another player process, a transcoder) does the
// decoding.
//
// Unlike the pipe backend this driver never sees decoded samples, so that
// driver's PCM concerns (sample format transform, clamping, volume scaling)
// do not exist here and audio_output_pipe_format does not apply.
type pipePassthroughOutput struct {
	reader librespot.AudioSourcePassthrough
	file   *os.File

	lock sync.Mutex
	cond *sync.Cond

	paused bool
	closed bool

	volumeUpdate chan float32
	err          chan error
}

func newPipePassthroughOutput(opts *NewOutputOptions) (*pipePassthroughOutput, error) {
	if len(opts.OutputPipe) == 0 {
		return nil, fmt.Errorf("audio_output_pipe must be set for the %s backend", BackendPipePassthrough)
	}

	reader, ok := opts.Reader.(librespot.AudioSourcePassthrough)
	if !ok {
		return nil, fmt.Errorf("passthrough requires an AudioSourcePassthrough reader")
	}

	out := &pipePassthroughOutput{
		reader:       reader,
		err:          make(chan error, 2),
		volumeUpdate: opts.VolumeUpdate,
	}
	out.cond = sync.NewCond(&out.lock)

	// The FIFO is opened exactly like in the pipe driver; the few lines are
	// duplicated on purpose to keep the two drivers independent.
	//
	// Open the FIFO for writing as non-blocking to cause an error if there is no reader.
	var err error
	out.file, err = os.OpenFile(opts.OutputPipe, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open fifo: %w", err)
	}

	// Restore blocking mode now that we are sure we have a reader.
	if err := syscall.SetNonblock(int(out.file.Fd()), false); err != nil {
		return nil, fmt.Errorf("failed to set blocking mode on fifo: %w", err)
	}

	go out.outputLoop()

	return out, nil
}

func (out *pipePassthroughOutput) outputLoop() {
	buf := make([]byte, 16*1024)

	for {
		out.lock.Lock()

		for out.paused && !out.closed {
			out.cond.Wait()
		}

		if out.closed {
			out.lock.Unlock()
			break
		}

		n, err := out.reader.ReadBytes(buf)

		if n > 0 {
			if _, werr := out.file.Write(buf[:n]); werr != nil {
				out.err <- werr
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

func (out *pipePassthroughOutput) Pause() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed {
		return nil
	}

	out.paused = true
	out.cond.Signal()
	return nil
}

func (out *pipePassthroughOutput) Resume() error {
	out.lock.Lock()
	defer out.lock.Unlock()

	if out.closed {
		return nil
	}

	out.paused = false
	out.cond.Signal()
	return nil
}

// Drop is a no-op: the driver buffers nothing itself, bytes are handed to
// the pipe as soon as they are read.
func (out *pipePassthroughOutput) Drop() error {
	return nil
}

// DelayMs reports no delay: the encoded stream is opaque to this driver, so
// it cannot know how much audio the downstream decoder has buffered.
func (out *pipePassthroughOutput) DelayMs() (int64, error) {
	return 0, nil
}

// SetVolume only reports the volume back on the update channel: volume
// scaling is an operation on decoded samples, the encoded stream is written
// untouched and the downstream consumer is in charge of volume.
func (out *pipePassthroughOutput) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}

	sendVolumeUpdate(out.volumeUpdate, vol)
}

func (out *pipePassthroughOutput) Error() <-chan error {
	// No need to lock here (out.err is only set in newPipePassthroughOutput).
	return out.err
}

func (out *pipePassthroughOutput) Close() error {
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
