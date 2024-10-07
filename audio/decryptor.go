package audio

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"io"
)

type Decryptor struct {
	reader io.ReaderAt
	cipher cipher.Block
}

var baseIv = []byte{0x72, 0xe0, 0x67, 0xfb, 0xdd, 0xcb, 0xcf, 0x77, 0xeb, 0xe8, 0xbc, 0x64, 0x3f, 0x63, 0x0d, 0x93}
var throwawayBuffer = make([]byte, aes.BlockSize)

func NewAesAudioDecryptor(r io.ReaderAt, key []byte) (*Decryptor, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return &Decryptor{r, c}, nil
}

func (a *Decryptor) ReadAt(p []byte, pos int64) (n int, err error) {
	bs := int64(a.cipher.BlockSize())
	block, off := uint32(pos/bs), int(pos%bs)

	// Create a new IV with an incremented counter.
	counter := uint32(baseIv[15]) + uint32(baseIv[14])<<8 + uint32(baseIv[13])<<16 + uint32(baseIv[12])<<24
	counter += block
	newIv := bytes.Clone(baseIv)
	newIv[15] = uint8(counter >> 0)
	newIv[14] = uint8(counter >> 8)
	newIv[13] = uint8(counter >> 16)
	newIv[12] = uint8(counter >> 24)

	stream := cipher.NewCTR(a.cipher, newIv)

	// read some bytes to throw away
	if off > 0 {
		stream.XORKeyStream(throwawayBuffer, throwawayBuffer[:off])
	}

	// read from source and decrypt
	n, err = a.reader.ReadAt(p, pos)
	if n > 0 {
		stream.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (a *Decryptor) Close() error {
	if closer, ok := a.reader.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}
