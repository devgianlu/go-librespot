package impl

import (
	"fmt"
)

type Impl struct {
}

func (p Impl) IsSupported() bool {
	return false
}

func (p Impl) GetVersion() int32 {
	return 0
}

func (p Impl) GetToken() []byte {
	return nil
}

func (p Impl) Deobfuscate(_, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("playplay plugin not provided")
}
