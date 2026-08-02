package daemon

import "time"

// Config carries the runtime configuration for a daemon instance.
type Config struct {
	DeviceId    string
	DeviceName  string
	DeviceType  string
	ClientToken string

	AudioBackend              string
	AudioBackendRuntimeSocket string
	AudioDevice               string
	MixerDevice               string
	MixerControlName          string
	AudioBufferTime           int
	AudioPeriodCount          int
	AudioOutputPipe           string
	AudioOutputPipeFormat     string

	Bitrate                   int
	VolumeSteps               uint32
	InitialVolume             uint32
	IgnoreLastVolume          bool
	NormalisationDisabled     bool
	NormalisationUseAlbumGain bool
	NormalisationPregain      float32
	CrossfadeDuration         int
	ExternalVolume            bool
	DisableAutoplay           bool

	// SkipDebounce is how long to wait after a burst of next/prev commands
	// before actually loading the track the pointer landed on, so mashing the
	// skip button costs one audio-key request instead of one per press. Zero
	// disables debouncing (every skip loads immediately).
	SkipDebounce time.Duration

	ZeroconfEnabled               bool
	ZeroconfPort                  int
	ZeroconfBackend               string
	ZeroconfInterfacesToAdvertise []string

	FlacEnabled bool

	// ImageSize selects which cover-art image variant the API server returns:
	// "default", "small", "medium", "large", "xlarge".
	ImageSize string

	Cache CacheConfig

	Credentials CredentialsConfig
}

// CacheConfig configures the on-disk cache for downloaded (encrypted) audio
// files.
type CacheConfig struct {
	// Enabled turns the audio file cache on or off.
	Enabled bool
	// Dir is the directory the cache is stored in.
	Dir string
	// SizeLimit is the maximum total size of the cached audio files in bytes.
	// A value of zero disables eviction (unbounded cache).
	SizeLimit int64
}

type CredentialsConfig struct {
	Type         string
	Interactive  InteractiveCredentials
	SpotifyToken SpotifyTokenCredentials
	Zeroconf     ZeroconfCredentials
}

type InteractiveCredentials struct {
	CallbackPort int
}

type SpotifyTokenCredentials struct {
	Username    string
	AccessToken string
}

type ZeroconfCredentials struct {
	PersistCredentials bool
}
