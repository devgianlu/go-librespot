package audio_key

import (
	"crypto/aes"
	"crypto/cipher"
	"io"
)

type AudioDecryptor struct {
	reader io.Reader
	cipher cipher.Block

	stream cipher.Stream
}

var baseIv []byte

func init() {
	baseIv = []byte{0x72, 0xe0, 0x67, 0xfb, 0xdd, 0xcb, 0xcf, 0x77, 0xeb, 0xe8, 0xbc, 0x64, 0x3f, 0x63, 0x0d, 0x93}
}

func NewAesAudioDecryptor(r io.Reader, key []byte) (*AudioDecryptor, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return &AudioDecryptor{r, c, cipher.NewCTR(c, baseIv)}, err
}

func (a *AudioDecryptor) Read(p []byte) (n int, err error) {
	n, err = a.reader.Read(p)
	if n > 0 {
		a.stream.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}
