//go:build darwin && test_unit

package output

// #cgo LDFLAGS: -framework AudioToolbox
// #include <AudioToolbox/AudioToolbox.h>
import "C"
import "fmt"

// cgo cannot be imported from a _test.go file. Keep the native parameter
// check behind the same build tag as the tests instead of adding a public API.
func toolboxVolumeForTest(out *toolboxOutput) (float32, error) {
	var volume C.AudioQueueParameterValue
	status := C.AudioQueueGetParameter(out.audioQueue, C.kAudioQueueParam_Volume, &volume)
	if status != C.noErr {
		return 0, fmt.Errorf("AudioQueueGetParameter: %d", status)
	}
	return float32(volume), nil
}
