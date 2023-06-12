package ap

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

var dhGenerator *big.Int
var dhPrime *big.Int

func init() {
	dhGenerator = big.NewInt(2)
	dhPrime = new(big.Int).SetBytes([]byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xc9, 0x0f, 0xda, 0xa2, 0x21, 0x68, 0xc2, 0x34, 0xc4, 0xc6,
		0x62, 0x8b, 0x80, 0xdc, 0x1c, 0xd1, 0x29, 0x02, 0x4e, 0x08, 0x8a, 0x67, 0xcc, 0x74, 0x02, 0x0b, 0xbe, 0xa6,
		0x3b, 0x13, 0x9b, 0x22, 0x51, 0x4a, 0x08, 0x79, 0x8e, 0x34, 0x04, 0xdd, 0xef, 0x95, 0x19, 0xb3, 0xcd, 0x3a,
		0x43, 0x1b, 0x30, 0x2b, 0x0a, 0x6d, 0xf2, 0x5f, 0x14, 0x37, 0x4f, 0xe1, 0x35, 0x6d, 0x6d, 0x51, 0xc2, 0x45,
		0xe4, 0x85, 0xb5, 0x76, 0x62, 0x5e, 0x7e, 0xc6, 0xf4, 0x4c, 0x42, 0xe9, 0xa6, 0x3a, 0x36, 0x20, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	})
}

type diffieHellman struct {
	privateKey *big.Int
	publicKey  *big.Int

	sharedSecret []byte
}

func newDiffieHellman() (dh *diffieHellman, err error) {
	privateKeyData := make([]byte, 95)
	if _, err = rand.Read(privateKeyData); err != nil {
		return nil, fmt.Errorf("failed reading random private key: %w", err)
	}

	dh = &diffieHellman{}
	dh.privateKey = new(big.Int).SetBytes(privateKeyData)
	dh.publicKey = new(big.Int).Exp(dhGenerator, dh.privateKey, dhPrime)
	return dh, nil
}

func (dh *diffieHellman) exchange(remoteKeyBytes []byte) []byte {
	remoteKey := new(big.Int).SetBytes(remoteKeyBytes)
	dh.sharedSecret = new(big.Int).Exp(remoteKey, dh.privateKey, dhPrime).Bytes()
	return dh.sharedSecret
}

func (dh *diffieHellman) publicKeyBytes() []byte {
	return dh.publicKey.Bytes()
}
