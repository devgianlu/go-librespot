package go_librespot

type Float32Reader interface {
	// Read reads 32bit little endian floats from the stream
	// until EOF or ErrDrainReader is returned.
	Read([]float32) (n int, err error)
}

type AudioSource interface {
	// SetPositionMs sets the new position in samples
	SetPositionMs(int64) error

	// PositionMs gets the position in samples
	PositionMs() int64

	// Read reads 32bit little endian floats from the stream
	Read([]float32) (int, error)
}
