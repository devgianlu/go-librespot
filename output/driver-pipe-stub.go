//go:build windows

package output

import (
	"fmt"
)

func newPipeOutput(opts *NewOutputOptions) (Output, error) {
	return nil, fmt.Errorf("pipe output is not supported on Windows")
}
