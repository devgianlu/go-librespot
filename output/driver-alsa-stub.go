//go:build darwin

package output

import (
	"fmt"

	librespot "github.com/devgianlu/go-librespot"
)

func newAlsaOutput(reader librespot.Float32Reader, sampleRate, channelCount int, device, mixer, control string, initialVolume float32, externalVolume bool, volumeUpdate chan float32) (Output, error) {
	return nil, fmt.Errorf("ALSA output is not supported on MacOS")
}
