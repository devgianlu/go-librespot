//go:build darwin && test_unit

package output

// #cgo LDFLAGS: -framework AudioToolbox
// #include <AudioToolbox/AudioToolbox.h>
import "C"
import (
	"fmt"
	"unsafe"
)

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

func toolboxRunningForTest(out *toolboxOutput) (bool, error) {
	var running C.UInt32
	size := C.UInt32(unsafe.Sizeof(running))
	status := C.AudioQueueGetProperty(out.audioQueue, C.kAudioQueueProperty_IsRunning, unsafe.Pointer(&running), &size)
	if status != C.noErr {
		return false, fmt.Errorf("AudioQueueGetProperty(IsRunning): %d", status)
	}
	return running != 0, nil
}
