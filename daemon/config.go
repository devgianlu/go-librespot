package daemon

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

	ZeroconfEnabled               bool
	ZeroconfPort                  int
	ZeroconfBackend               string
	ZeroconfInterfacesToAdvertise []string

	FlacEnabled bool

	// ImageSize selects which cover-art image variant the API server returns:
	// "default", "small", "medium", "large", "xlarge".
	ImageSize string

	Cache CacheConfig

	Metadata MetadataConfig

	Credentials CredentialsConfig
}

// MetadataConfig configures the in-memory track metadata cache behind the
// next_track status field and the /context/tracks listing. Everything here is
// opt-in: a headless speaker has no use for metadata beyond the playing track
// and should not pay network requests for it.
type MetadataConfig struct {
	// Enabled turns on the metadata cache, the batched fetch of metadata for
	// the tracks around the playback position, and the /context/tracks
	// endpoint. Off, the daemon performs no metadata request playback does not
	// need.
	Enabled bool
	// ContextSweep additionally resolves metadata for the whole context when
	// one starts playing, so every track is known before the user skips
	// anywhere. Requires Enabled.
	ContextSweep bool
	// MaxTracks caps how many tracks of a context are enumerated and swept.
	MaxTracks int
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
