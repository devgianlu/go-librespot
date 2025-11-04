//go:build !playplay

package playplay

import (
	"fmt"

	"github.com/devgianlu/go-librespot/playplay/plugin"
)

var Plugin plugin.Interface = dummyPlugin{}

type dummyPlugin struct {
}

func (p dummyPlugin) IsSupported() bool {
	return false
}

func (p dummyPlugin) GetVersion() int32 {
	return 0
}

func (p dummyPlugin) GetToken() []byte {
	return nil
}

func (p dummyPlugin) Deobfuscate(_, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("playplay plugin not provided")
}
