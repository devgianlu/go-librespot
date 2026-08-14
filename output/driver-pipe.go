package output

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

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

	transform func([]float32, []byte) int
}

// Largest float that scales into an int16 without wrapping.
const maxSampleValueS16 = float32(0x7fff) / float32(0x8000)

func clampSample(f, max float32) float32 {
	if f < -1 {
		return -1
	}
	if f > max {
		return max
	}
	return f
}

func newPipeTransform(format string) (func([]float32, []byte) int, error) {
	switch format {
	case "s16le":
		return func(in []float32, out []byte) int {
			for i := 0; i < len(in); i++ {
				sample := int16(clampSample(in[i], maxSampleValueS16) * 32768)
				binary.LittleEndian.PutUint16(out[i*2:], uint16(sample))
			}
			return len(in) * 2
		}, nil
	case "s32le":
		// float32 rounds 2147483647 up to 2^31, so scale in float64.
		return func(in []float32, out []byte) int {
			for i := 0; i < len(in); i++ {
				sample := int32(float64(clampSample(in[i], 1)) * 2147483647)
				binary.LittleEndian.PutUint32(out[i*4:], uint32(sample))
			}
			return len(in) * 4
		}, nil
	case "f32le":
		return func(in []float32, out []byte) int {
			for i := 0; i < len(in); i++ {
				sample := math.Float32bits(in[i])
				binary.LittleEndian.PutUint32(out[i*4:], sample)
			}
			return len(in) * 4
		}, nil
	default:
		return nil, fmt.Errorf("unknown output pipe format: %s", format)
	}
}

func (out *pipeOutput) outputLoop() {
	floats := make([]float32, 4*1024)
	bytes := make([]byte, 4*len(floats)) // times four is the biggest we can get

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
			nn := out.transform(floats[:n], bytes)
			_, err := out.file.Write(bytes[:nn])
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
