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

	Credentials CredentialsConfig
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
