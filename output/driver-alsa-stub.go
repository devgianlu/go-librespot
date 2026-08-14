//go:build android || darwin || js || windows || nintendosdk

package output

import (
	"fmt"
)

func newAlsaOutput(opts *NewOutputOptions) (Output, error) {
	return nil, fmt.Errorf("alsa output is not supported on this platform")
}
