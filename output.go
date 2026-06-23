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

// AudioSourcePassthrough is an AudioSource that can additionally hand out the
// raw encoded (Ogg/Vorbis) bytes instead of decoded float32 samples. It backs
// the pipe backend's passthrough mode: the decoder is bypassed and the
// container is written to the pipe untouched, so a downstream consumer (e.g.
// a hardware decoder) does the decoding. ReadBytes and Read must not be mixed
// on the same source.
type AudioSourcePassthrough interface {
	AudioSource

	// ReadBytes reads the raw encoded stream until EOF.
	ReadBytes([]byte) (int, error)
}
