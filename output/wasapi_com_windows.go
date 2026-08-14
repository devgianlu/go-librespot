//go:build windows

package output

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// COM / WASAPI constants from mmdeviceapi.h, audioclient.h, and mmreg.h.
const (
	clsctxAll = 0x17 // CLSCTX_INPROC_SERVER|INPROC_HANDLER|LOCAL_SERVER|REMOTE_SERVER

	eRender  = 0
	eConsole = 0

	audclntSharemodeShared = 0

	audclntStreamflagsEventcallback  = 0x00040000
	audclntStreamflagsNopersist      = 0x00080000
	audclntStreamflagsSrcDefaultQual = 0x08000000
	audclntStreamflagsAutoconvertPCM = 0x80000000

	waveFormatTagExtensible = 0xFFFE
	speakerFrontLeft        = 0x1
	speakerFrontRight       = 0x2
	speakerFrontCenter      = 0x4

	audclntENotStopped        = 0x88890024
	audclntEDeviceInvalidated = 0x88890004
)

// Interface IDs from the Windows SDK headers (mmdeviceapi.h, audioclient.h).
var (
	clsidMMDeviceEnumerator = windows.GUID{
		Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c,
		Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e},
	}
	iidIMMDeviceEnumerator = windows.GUID{
		Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35,
		Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6},
	}
	iidIAudioClient = windows.GUID{
		Data1: 0x1cb9ad4c, Data2: 0xdbfa, Data3: 0x4c32,
		Data4: [8]byte{0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2},
	}
	iidIAudioRenderClient = windows.GUID{
		Data1: 0xf294acfc, Data2: 0x3146, Data3: 0x4483,
		Data4: [8]byte{0xa7, 0xbf, 0xad, 0xdc, 0xa7, 0xc2, 0x60, 0xe2},
	}
	// KSDATAFORMAT_SUBTYPE_IEEE_FLOAT
	ksDataFormatSubtypeIEEEFloat = windows.GUID{
		Data1: 0x00000003, Data2: 0x0000, Data3: 0x0010,
		Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71},
	}
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
)

type hresult uint32

func (hr hresult) Error() string {
	return fmt.Sprintf("wasapi HRESULT 0x%08x", uint32(hr))
}

func hrError(r uintptr) error {
	if r == 0 || r == 1 { // S_OK, S_FALSE
		return nil
	}
	return hresult(r)
}

func isHRESULT(err error, code uint32) bool {
	hr, ok := err.(hresult)
	return ok && uint32(hr) == code
}

// WAVEFORMATEXTENSIBLE as defined in mmreg.h.
type waveFormatExtensible struct {
	formatTag      uint16
	channels       uint16
	samplesPerSec  uint32
	avgBytesPerSec uint32
	blockAlign     uint16
	bitsPerSample  uint16
	cbSize         uint16
	validBits      uint16
	channelMask    uint32
	subFormat      windows.GUID
}

func newFloatFormat(sampleRate, channels int) *waveFormatExtensible {
	const bits = 32
	block := channels * bits / 8
	mask := uint32(speakerFrontCenter)
	if channels == 2 {
		mask = speakerFrontLeft | speakerFrontRight
	}
	return &waveFormatExtensible{
		formatTag:      waveFormatTagExtensible,
		channels:       uint16(channels),
		samplesPerSec:  uint32(sampleRate),
		avgBytesPerSec: uint32(sampleRate * block),
		blockAlign:     uint16(block),
		bitsPerSample:  bits,
		cbSize:         22,
		validBits:      bits,
		channelMask:    mask,
		subFormat:      ksDataFormatSubtypeIEEEFloat,
	}
}

type iUnknownVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
}

type immDeviceEnumeratorVtbl struct {
	iUnknownVtbl
	enumAudioEndpoints                     uintptr
	getDefaultAudioEndpoint                uintptr
	getDevice                              uintptr
	registerEndpointNotificationCallback   uintptr
	unregisterEndpointNotificationCallback uintptr
}

type immDeviceVtbl struct {
	iUnknownVtbl
	activate          uintptr
	openPropertyStore uintptr
	getId             uintptr
	getState          uintptr
}

type iAudioClientVtbl struct {
	iUnknownVtbl
	initialize        uintptr
	getBufferSize     uintptr
	getStreamLatency  uintptr
	getCurrentPadding uintptr
	isFormatSupported uintptr
	getMixFormat      uintptr
	getDevicePeriod   uintptr
	start             uintptr
	stop              uintptr
	reset             uintptr
	setEventHandle    uintptr
	getService        uintptr
}

type iAudioRenderClientVtbl struct {
	iUnknownVtbl
	getBuffer     uintptr
	releaseBuffer uintptr
}

type immDeviceEnumerator struct{ vtbl *immDeviceEnumeratorVtbl }
type immDevice struct{ vtbl *immDeviceVtbl }
type iAudioClient struct{ vtbl *iAudioClientVtbl }
type iAudioRenderClient struct{ vtbl *iAudioRenderClientVtbl }

func comRelease(release uintptr, obj unsafe.Pointer) {
	if obj == nil || release == 0 {
		return
	}
	_, _, _ = syscallN(release, uintptr(obj))
}

func syscallN(trap uintptr, args ...uintptr) (uintptr, uintptr, error) {
	return syscall.SyscallN(trap, args...)
}

// Thin wrappers so the driver never touches vtable slots directly.

func coCreateMMDeviceEnumerator() (*immDeviceEnumerator, error) {
	var unk unsafe.Pointer
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&unk)),
	)
	if err := hrError(r); err != nil {
		return nil, fmt.Errorf("CoCreateInstance(MMDeviceEnumerator): %w", err)
	}
	return (*immDeviceEnumerator)(unk), nil
}

func (e *immDeviceEnumerator) GetDefaultAudioEndpoint() (*immDevice, error) {
	var dev *immDevice
	r, _, _ := syscallN(e.vtbl.getDefaultAudioEndpoint,
		uintptr(unsafe.Pointer(e)),
		eRender,
		eConsole,
		uintptr(unsafe.Pointer(&dev)),
	)
	if err := hrError(r); err != nil {
		return nil, fmt.Errorf("IMMDeviceEnumerator.GetDefaultAudioEndpoint: %w", err)
	}
	return dev, nil
}

func (e *immDeviceEnumerator) Release() {
	if e != nil {
		comRelease(e.vtbl.release, unsafe.Pointer(e))
	}
}

func (d *immDevice) ActivateAudioClient() (*iAudioClient, error) {
	var client *iAudioClient
	r, _, _ := syscallN(d.vtbl.activate,
		uintptr(unsafe.Pointer(d)),
		uintptr(unsafe.Pointer(&iidIAudioClient)),
		clsctxAll,
		0,
		uintptr(unsafe.Pointer(&client)),
	)
	if err := hrError(r); err != nil {
		return nil, fmt.Errorf("IMMDevice.Activate(IAudioClient): %w", err)
	}
	return client, nil
}

func (d *immDevice) Release() {
	if d != nil {
		comRelease(d.vtbl.release, unsafe.Pointer(d))
	}
}

func (c *iAudioClient) Initialize(bufferDuration100ns int64, format *waveFormatExtensible) error {
	flags := uintptr(audclntStreamflagsEventcallback | audclntStreamflagsNopersist |
		audclntStreamflagsAutoconvertPCM | audclntStreamflagsSrcDefaultQual)
	r, _, _ := syscallN(c.vtbl.initialize,
		uintptr(unsafe.Pointer(c)),
		audclntSharemodeShared,
		flags,
		uintptr(bufferDuration100ns),
		0,
		uintptr(unsafe.Pointer(format)),
		0,
	)
	if err := hrError(r); err != nil {
		return fmt.Errorf("IAudioClient.Initialize: %w", err)
	}
	return nil
}

func (c *iAudioClient) GetBufferSize() (uint32, error) {
	var frames uint32
	r, _, _ := syscallN(c.vtbl.getBufferSize, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&frames)))
	if err := hrError(r); err != nil {
		return 0, fmt.Errorf("IAudioClient.GetBufferSize: %w", err)
	}
	return frames, nil
}

func (c *iAudioClient) GetCurrentPadding() (uint32, error) {
	var frames uint32
	r, _, _ := syscallN(c.vtbl.getCurrentPadding, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&frames)))
	if err := hrError(r); err != nil {
		return 0, fmt.Errorf("IAudioClient.GetCurrentPadding: %w", err)
	}
	return frames, nil
}

func (c *iAudioClient) Start() error {
	r, _, _ := syscallN(c.vtbl.start, uintptr(unsafe.Pointer(c)))
	if err := hrError(r); err != nil && !isHRESULT(err, audclntENotStopped) {
		return fmt.Errorf("IAudioClient.Start: %w", err)
	}
	return nil
}

func (c *iAudioClient) Stop() error {
	r, _, _ := syscallN(c.vtbl.stop, uintptr(unsafe.Pointer(c)))
	if err := hrError(r); err != nil {
		return fmt.Errorf("IAudioClient.Stop: %w", err)
	}
	return nil
}

func (c *iAudioClient) Reset() error {
	r, _, _ := syscallN(c.vtbl.reset, uintptr(unsafe.Pointer(c)))
	if err := hrError(r); err != nil {
		return fmt.Errorf("IAudioClient.Reset: %w", err)
	}
	return nil
}

func (c *iAudioClient) SetEventHandle(ev windows.Handle) error {
	r, _, _ := syscallN(c.vtbl.setEventHandle, uintptr(unsafe.Pointer(c)), uintptr(ev))
	if err := hrError(r); err != nil {
		return fmt.Errorf("IAudioClient.SetEventHandle: %w", err)
	}
	return nil
}

func (c *iAudioClient) GetRenderClient() (*iAudioRenderClient, error) {
	var rc *iAudioRenderClient
	r, _, _ := syscallN(c.vtbl.getService,
		uintptr(unsafe.Pointer(c)),
		uintptr(unsafe.Pointer(&iidIAudioRenderClient)),
		uintptr(unsafe.Pointer(&rc)),
	)
	if err := hrError(r); err != nil {
		return nil, fmt.Errorf("IAudioClient.GetService(IAudioRenderClient): %w", err)
	}
	return rc, nil
}

func (c *iAudioClient) Release() {
	if c != nil {
		comRelease(c.vtbl.release, unsafe.Pointer(c))
	}
}

func (r *iAudioRenderClient) GetBuffer(frames uint32) (*byte, error) {
	var buf *byte
	hr, _, _ := syscallN(r.vtbl.getBuffer,
		uintptr(unsafe.Pointer(r)),
		uintptr(frames),
		uintptr(unsafe.Pointer(&buf)),
	)
	if err := hrError(hr); err != nil {
		return nil, fmt.Errorf("IAudioRenderClient.GetBuffer: %w", err)
	}
	return buf, nil
}

func (r *iAudioRenderClient) ReleaseBuffer(frames uint32) error {
	hr, _, _ := syscallN(r.vtbl.releaseBuffer, uintptr(unsafe.Pointer(r)), uintptr(frames), 0)
	if err := hrError(hr); err != nil {
		return fmt.Errorf("IAudioRenderClient.ReleaseBuffer: %w", err)
	}
	return nil
}

func (r *iAudioRenderClient) Release() {
	if r != nil {
		comRelease(r.vtbl.release, unsafe.Pointer(r))
	}
}
