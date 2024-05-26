package go_librespot

import "errors"

var ErrDrainReader = errors.New("please drain output")

type Float32Reader interface {
	// Read reads 32bit little endian floats from the stream
	// until EOF or ErrDrainReader is returned.
	Read([]float32) (n int, err error)

	// Drained must be called when Read returns ErrDrainReader and
	// the reader the has finished draining the output.
	Drained()
}

type AudioSource interface {
	// SetPositionMs sets the new position in samples
	SetPositionMs(int64) error

	// PositionMs gets the position in samples
	PositionMs() int64

	// Read reads 32bit little endian floats from the stream
	Read([]float32) (int, error)
}
