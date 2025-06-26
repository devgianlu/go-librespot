//go:build !darwin

package output

import (
	"fmt"

	librespot "github.com/devgianlu/go-librespot"
)

func newAudioToolboxOutput(reader librespot.Float32Reader, sampleRate, channelCount int, initialVolume float32) (Output, error) {
	return nil, fmt.Errorf("audio toolbox is only supported on MacOS")

}
