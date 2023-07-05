package audio

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"io"
)

type Decryptor struct {
	reader io.ReadSeeker

	cipher cipher.Block
	stream cipher.Stream
}

var baseIv = []byte{0x72, 0xe0, 0x67, 0xfb, 0xdd, 0xcb, 0xcf, 0x77, 0xeb, 0xe8, 0xbc, 0x64, 0x3f, 0x63, 0x0d, 0x93}
var throwawayBuffer = make([]byte, aes.BlockSize)

func NewAesAudioDecryptor(r io.ReadSeeker, key []byte) (*Decryptor, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return &Decryptor{r, c, cipher.NewCTR(c, baseIv)}, nil
}

func (a *Decryptor) Read(p []byte) (n int, err error) {
	n, err = a.reader.Read(p)
	if n > 0 {
		a.stream.XORKeyStream(p[:n], p[:n])
		println("read dec", n, hex.EncodeToString(p[:n]))
	}
	return n, err
}

func (a *Decryptor) Seek(offset int64, whence int) (int64, error) {
	offsetStart, err := a.reader.Seek(offset, whence)
	if err != nil {
		return offsetStart, err
	}

	bs := int64(a.cipher.BlockSize())
	blocks := offsetStart / bs
	leftover := offsetStart % bs

	newIv := bytes.Clone(baseIv)
	for j := int64(0); j < blocks; j++ {
		for i := len(newIv) - 1; i >= 0; i-- {
			newIv[i]++
			if newIv[i] != 0 {
				break
			}
		}
	}

	a.stream = cipher.NewCTR(a.cipher, newIv)

	// read some bytes to throw away
	if leftover > 0 {
		a.stream.XORKeyStream(throwawayBuffer, throwawayBuffer[:leftover])
	}

	return offsetStart, nil
}

func (a *Decryptor) Close() error {
	if closer, ok := a.reader.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}
