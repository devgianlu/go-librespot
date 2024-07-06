package output

import (
	"encoding/binary"
	"math"
	librespot "go-librespot"
)

// Float32ToByteReader wraps a Float32Reader to convert float32 samples to byte samples.
type Float32ToByteReader struct {
	Float32Reader librespot.Float32Reader
	buffer        []byte
}

// NewFloat32ToByteReader creates a new Float32ToByteReader.
func NewFloat32ToByteReader(reader librespot.Float32Reader) *Float32ToByteReader {
	return &Float32ToByteReader{
		Float32Reader: reader,
		buffer:        make([]byte, 0),
	}
}

// Read reads byte samples from the wrapped Float32Reader.
func (r *Float32ToByteReader) Read(p []byte) (int, error) {
	if len(r.buffer) == 0 {
		floatBuffer := make([]float32, len(p)/4)
		n, err := r.Float32Reader.Read(floatBuffer)
		if err != nil {
			return 0, err
		}

		r.buffer = make([]byte, n*4)
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint32(r.buffer[i*4:], math.Float32bits(floatBuffer[i]))
		}
	}

	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]

	return n, nil
}

