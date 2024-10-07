package audio

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
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
	block, off := uint64(pos/bs), int(pos%bs)

	// Create a new IV with an incremented counter.
	// Because the IV is static, we can just increment the last 8 bytes (64
	// bits) knowing it won't overflow with any reasonable stream size. With the
	// current base IV it will overflow at over 20 exabyte.
	counter := binary.BigEndian.Uint64(baseIv[8:])
	counter += block
	newIv := bytes.Clone(baseIv)
	binary.BigEndian.PutUint64(newIv[8:], counter)

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
