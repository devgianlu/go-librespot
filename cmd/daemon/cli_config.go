package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devgianlu/go-librespot/daemon"
	"github.com/gofrs/flock"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
)

var errAlreadyRunning = errors.New("go-librespot is already running")

type cliConfig struct {
	ConfigDir string `koanf:"config_dir"`

	// Keep this around so the lockfile finalizer doesn't release it.
	configLock *flock.Flock

	LogLevel            log.Level `koanf:"log_level"`
	LogDisableTimestamp bool      `koanf:"log_disable_timestamp"`

	DeviceId    string `koanf:"device_id"`
	DeviceName  string `koanf:"device_name"`
	DeviceType  string `koanf:"device_type"`
	ClientToken string `koanf:"client_token"`

	AudioBackend              string `koanf:"audio_backend"`
	AudioBackendRuntimeSocket string `koanf:"audio_backend_runtime_socket"`
	AudioDevice               string `koanf:"audio_device"`
	MixerDevice               string `koanf:"mixer_device"`
	MixerControlName          string `koanf:"mixer_control_name"`
	AudioBufferTime           int    `koanf:"audio_buffer_time"`
	AudioPeriodCount          int    `koanf:"audio_period_count"`
	AudioOutputPipe           string `koanf:"audio_output_pipe"`
	AudioOutputPipeFormat     string `koanf:"audio_output_pipe_format"`

	Bitrate                       int      `koanf:"bitrate"`
	VolumeSteps                   uint32   `koanf:"volume_steps"`
	InitialVolume                 uint32   `koanf:"initial_volume"`
	IgnoreLastVolume              bool     `koanf:"ignore_last_volume"`
	NormalisationDisabled         bool     `koanf:"normalisation_disabled"`
	NormalisationUseAlbumGain     bool     `koanf:"normalisation_use_album_gain"`
	NormalisationPregain          float32  `koanf:"normalisation_pregain"`
	CrossfadeDuration             int      `koanf:"crossfade_duration"`
	ExternalVolume                bool     `koanf:"external_volume"`
	ZeroconfEnabled               bool     `koanf:"zeroconf_enabled"`
	ZeroconfPort                  int      `koanf:"zeroconf_port"`
	ZeroconfBackend               string   `koanf:"zeroconf_backend"`
	DisableAutoplay               bool     `koanf:"disable_autoplay"`
	ZeroconfInterfacesToAdvertise []string `koanf:"zeroconf_interfaces_to_advertise"`
	MprisEnabled                  bool     `koanf:"mpris_enabled"`
	FlacEnabled                   bool     `koanf:"flac_enabled"`

	Server struct {
		Enabled     bool   `koanf:"enabled"`
		Address     string `koanf:"address"`
		Port        int    `koanf:"port"`
		AllowOrigin string `koanf:"allow_origin"`
		CertFile    string `koanf:"cert_file"`
		KeyFile     string `koanf:"key_file"`

		ImageSize string `koanf:"image_size"`
	} `koanf:"server"`

	Credentials struct {
		Type        string `koanf:"type"`
		Interactive struct {
			CallbackPort int `koanf:"callback_port"`
		} `koanf:"interactive"`
		SpotifyToken struct {
			Username    string `koanf:"username"`
			AccessToken string `koanf:"access_token"`
		} `koanf:"spotify_token"`
		Zeroconf struct {
			PersistCredentials bool `koanf:"persist_credentials"`
		} `koanf:"zeroconf"`
	} `koanf:"credentials"`
}

func (c *cliConfig) toDaemonConfig() *daemon.Config {
	dc := &daemon.Config{
		DeviceId:    c.DeviceId,
		DeviceName:  c.DeviceName,
		DeviceType:  c.DeviceType,
		ClientToken: c.ClientToken,

		AudioBackend:              c.AudioBackend,
		AudioBackendRuntimeSocket: c.AudioBackendRuntimeSocket,
		AudioDevice:               c.AudioDevice,
		MixerDevice:               c.MixerDevice,
		MixerControlName:          c.MixerControlName,
		AudioBufferTime:           c.AudioBufferTime,
		AudioPeriodCount:          c.AudioPeriodCount,
		AudioOutputPipe:           c.AudioOutputPipe,
		AudioOutputPipeFormat:     c.AudioOutputPipeFormat,

		Bitrate:                   c.Bitrate,
		VolumeSteps:               c.VolumeSteps,
		InitialVolume:             c.InitialVolume,
		IgnoreLastVolume:          c.IgnoreLastVolume,
		NormalisationDisabled:     c.NormalisationDisabled,
		NormalisationUseAlbumGain: c.NormalisationUseAlbumGain,
		NormalisationPregain:      c.NormalisationPregain,
		CrossfadeDuration:         c.CrossfadeDuration,
		ExternalVolume:            c.ExternalVolume,
		DisableAutoplay:           c.DisableAutoplay,

		ZeroconfEnabled:               c.ZeroconfEnabled,
		ZeroconfPort:                  c.ZeroconfPort,
		ZeroconfBackend:               c.ZeroconfBackend,
		ZeroconfInterfacesToAdvertise: c.ZeroconfInterfacesToAdvertise,

		FlacEnabled: c.FlacEnabled,
		ImageSize:   c.Server.ImageSize,
	}
	dc.Credentials.Type = c.Credentials.Type
	dc.Credentials.Interactive.CallbackPort = c.Credentials.Interactive.CallbackPort
	dc.Credentials.SpotifyToken.Username = c.Credentials.SpotifyToken.Username
	dc.Credentials.SpotifyToken.AccessToken = c.Credentials.SpotifyToken.AccessToken
	dc.Credentials.Zeroconf.PersistCredentials = c.Credentials.Zeroconf.PersistCredentials
	return dc
}

func loadCLIConfig(cfg *cliConfig) error {
	f := flag.NewFlagSet("config", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Println(f.FlagUsages())
		os.Exit(0)
	}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	defaultConfigDir := filepath.Join(userConfigDir, "go-librespot")
	f.StringVar(&cfg.ConfigDir, "config_dir", defaultConfigDir, "the configuration directory")

	var configOverrides []string
	f.StringArrayVarP(&configOverrides, "conf", "c", nil, "override config values (format: field=value, use field1.field2=value for nested fields)")

	if err := f.Parse(os.Args[1:]); err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("failed creating config directory: %w", err)
	}

	lockFilePath := filepath.Join(cfg.ConfigDir, "lockfile")
	cfg.configLock = flock.New(lockFilePath)
	if locked, err := cfg.configLock.TryLock(); err != nil {
		return fmt.Errorf("could not lock config directory: %w", err)
	} else if !locked {
		return fmt.Errorf("%w (lockfile: %s)", errAlreadyRunning, lockFilePath)
	}

	k := koanf.New(".")

	_ = k.Load(confmap.Provider(map[string]interface{}{
		"log_level": log.InfoLevel,

		"device_type": "computer",
		"bitrate":     160,

		"audio_backend":            "alsa",
		"audio_device":             "default",
		"audio_output_pipe_format": "s16le",
		"mixer_control_name":       "Master",

		"volume_steps":   100,
		"initial_volume": 100,

		"credentials.type": "zeroconf",

		"zeroconf_backend": "builtin",

		"server.address":    "localhost",
		"server.image_size": "default",
	}, "."), nil)

	var configPath string
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "config.yaml")); os.IsNotExist(err) {
		configPath = filepath.Join(cfg.ConfigDir, "config.yml")
	} else {
		configPath = filepath.Join(cfg.ConfigDir, "config.yaml")
	}

	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed reading configuration file: %w", err)
		}
	}

	if err := k.Load(posflag.Provider(f, ".", k), nil); err != nil {
		return fmt.Errorf("failed loading command line configuration: %w", err)
	}

	if len(configOverrides) > 0 {
		overrideMap := make(map[string]interface{})
		for _, override := range configOverrides {
			parts := strings.SplitN(override, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid config override format: %s (expected field=value)", override)
			}
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if key == "" {
				return fmt.Errorf("invalid config override: empty field name in %s", override)
			}
			overrideMap[key] = value
		}
		if err := k.Load(confmap.Provider(overrideMap, "."), nil); err != nil {
			return fmt.Errorf("failed loading config overrides: %w", err)
		}
	}

	if err := k.Unmarshal("", &cfg); err != nil {
		return fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	if cfg.DeviceName == "" {
		cfg.DeviceName = "go-librespot"

		hostname, _ := os.Hostname()
		if hostname != "" {
			cfg.DeviceName += " " + hostname
		}
	}

	return nil
}
