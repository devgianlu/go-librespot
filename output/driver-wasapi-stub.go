//go:build !windows

package output

import (
	"fmt"
)

func newWasapiOutput(opts *NewOutputOptions) (Output, error) {
	return nil, fmt.Errorf("wasapi output is only supported on Windows")
}
