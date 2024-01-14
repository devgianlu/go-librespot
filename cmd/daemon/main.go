package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	log "github.com/sirupsen/logrus"
	"go-librespot/apresolve"
	"go-librespot/player"
	devicespb "go-librespot/proto/spotify/connectstate/devices"
	"go-librespot/session"
	"go-librespot/zeroconf"
	"golang.org/x/exp/rand"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
	"time"
)

type App struct {
	cfg *Config

	resolver *apresolve.ApResolver

	deviceId    string
	deviceType  devicespb.DeviceType
	clientToken string

	server *ApiServer
}

func parseDeviceType(val string) (devicespb.DeviceType, error) {
	valEnum, ok := devicespb.DeviceType_value[strings.ToUpper(val)]
	if !ok {
		return 0, fmt.Errorf("invalid device type: %s", val)
	}

	return devicespb.DeviceType(valEnum), nil
}

func NewApp(cfg *Config) (app *App, err error) {
	app = &App{cfg: cfg}

	app.deviceType, err = parseDeviceType(*cfg.DeviceType)
	if err != nil {
		return nil, err
	}

	app.resolver = apresolve.NewApResolver()

	if cfg.DeviceId == nil {
		deviceIdBytes := make([]byte, 20)
		_, _ = rand.Read(deviceIdBytes)
		app.deviceId = hex.EncodeToString(deviceIdBytes)
		log.Infof("generated new device id: %s", app.deviceId)
	} else {
		app.deviceId = *cfg.DeviceId
	}

	if cfg.ClientToken != nil {
		app.clientToken = *cfg.ClientToken
	}

	return app, nil
}

func (app *App) newAppPlayer(creds any) (_ *AppPlayer, err error) {
	appPlayer := &AppPlayer{
		app:         app,
		stop:        make(chan struct{}, 1),
		countryCode: new(string),
	}

	if appPlayer.sess, err = session.NewSessionFromOptions(&session.Options{
		DeviceType:  app.deviceType,
		DeviceId:    app.deviceId,
		ClientToken: app.clientToken,
		Resolver:    app.resolver,
		Credentials: creds,
	}); err != nil {
		return nil, err
	}

	appPlayer.initState()

	if appPlayer.player, err = player.NewPlayer(
		appPlayer.sess.Spclient(), appPlayer.sess.AudioKey(), app.cfg.NormalisationEnabled,
		*app.cfg.NormalisationPregain, appPlayer.countryCode, *app.cfg.AudioDevice,
		*app.cfg.VolumeSteps, app.cfg.ExternalVolume,
	); err != nil {
		return nil, fmt.Errorf("failed initializing player: %w", err)
	}

	return appPlayer, nil
}

func (app *App) Zeroconf() error {
	// pre fetch resolver endpoints
	if err := app.resolver.FetchAll(); err != nil {
		return fmt.Errorf("failed getting endpoints from resolver: %w", err)
	}

	// start zeroconf server and dispatch
	z, err := zeroconf.NewZeroconf(*app.cfg.DeviceName, app.deviceId, app.deviceType)
	if err != nil {
		return fmt.Errorf("failed initializing zeroconf: %w", err)
	}

	// TODO: unset this when logging out
	var currentPlayer *AppPlayer
	var apiCh chan ApiRequest

	// forward API requests to proper channel only if a session is present
	go func() {
		for {
			select {
			case req := <-app.server.Receive():
				if currentPlayer == nil {
					req.Reply(nil, ErrNoSession)
					break
				}

				// if we are here a channel must exist
				apiCh <- req
			}
		}
	}()

	return z.Serve(func(req zeroconf.NewUserRequest) bool {
		if currentPlayer != nil {
			currentPlayer.Close()
			currentPlayer = nil

			// close the channel after setting the current session to nil
			close(apiCh)
		}

		appPlayer, err := app.newAppPlayer(session.BlobCredentials{
			Username: req.Username,
			Blob:     req.AuthBlob,
		})
		if err != nil {
			log.WithError(err).Errorf("failed creating new session for %s from %s", req.Username, req.DeviceName)
			return false
		}

		// first create the channel and then assign the current session
		apiCh = make(chan ApiRequest)
		currentPlayer = appPlayer

		go appPlayer.Run(apiCh)
		return true
	})
}

type storedCredentialsFile struct {
	Username string `json:"username"`
	Data     []byte `json:"data"`
}

func (app *App) SpotifyToken(username, token string) error {
	return app.withReusableCredentials(session.SpotifyTokenCredentials{Username: username, Token: token})
}

func (app *App) UserPass(username, password string) error {
	return app.withReusableCredentials(session.UserPassCredentials{Username: username, Password: password})
}

func (app *App) withReusableCredentials(creds any) (err error) {
	var username string
	switch creds := creds.(type) {
	case session.SpotifyTokenCredentials:
		username = creds.Username
	case session.UserPassCredentials:
		username = creds.Username
	default:
		return fmt.Errorf("unsupported credentials for reuse")
	}

	var storedCredentials []byte
	if content, err := os.ReadFile(app.cfg.CredentialsPath); err == nil {
		var file storedCredentialsFile
		if err := json.Unmarshal(content, &file); err != nil {
			return fmt.Errorf("failed unmarshalling stored credentials file: %w", err)
		}

		log.Debugf("stored credentials found for %s", file.Username)
		if file.Username == username {
			storedCredentials = file.Data
		} else {
			log.Warnf("stored credentials found for wrong username %s != %s", file.Username, username)
		}
	} else {
		log.Debugf("stored credentials not found")
	}

	var appPlayer *AppPlayer
	if len(storedCredentials) > 0 {
		appPlayer, err = app.newAppPlayer(session.StoredCredentials{Username: username, Data: storedCredentials})
		if err != nil {
			return err
		}
	} else {
		appPlayer, err = app.newAppPlayer(creds)
		if err != nil {
			return err
		}

		if content, err := json.Marshal(&storedCredentialsFile{
			Username: appPlayer.sess.Username(),
			Data:     appPlayer.sess.StoredCredentials(),
		}); err != nil {
			return fmt.Errorf("failed marshalling stored credentials: %w", err)
		} else if err := os.WriteFile(app.cfg.CredentialsPath, content, 0600); err != nil {
			return fmt.Errorf("failed writing stored credentials file: %w", err)
		}

		log.Debugf("stored credentials for %s", appPlayer.sess.Username())
	}

	appPlayer.Run(app.server.Receive())
	return nil
}

type Config struct {
	ConfigPath      string `yaml:"-"`
	CredentialsPath string `yaml:"-"`

	LogLevel             *string  `yaml:"log_level"`
	DeviceId             *string  `yaml:"device_id"`
	DeviceName           *string  `yaml:"device_name"`
	DeviceType           *string  `yaml:"device_type"`
	ClientToken          *string  `yaml:"client_token"`
	ServerPort           *int     `yaml:"server_port"`
	AudioDevice          *string  `yaml:"audio_device"`
	Bitrate              *int     `yaml:"bitrate"`
	VolumeSteps          *uint32  `yaml:"volume_steps"`
	NormalisationEnabled bool     `yaml:"normalisation_enabled"`
	NormalisationPregain *float32 `yaml:"normalisation_pregain"`
	ExternalVolume       bool     `yaml:"external_volume"`
	Credentials          struct {
		Type     string `yaml:"type"`
		UserPass struct {
			Username string `yaml:"username"`
			Password string `yaml:"password"`
		} `yaml:"user_pass"`
		SpotifyToken struct {
			Username    string `yaml:"username"`
			AccessToken string `yaml:"access_token"`
		} `yaml:"spotify_token"`
	} `yaml:"credentials"`
}

func loadConfig(cfg *Config) error {
	flag.StringVar(&cfg.ConfigPath, "config_path", "config.yml", "the configuration file path")
	flag.StringVar(&cfg.CredentialsPath, "credentials_path", "credentials.json", "the credentials file path")
	flag.Parse()

	configBytes, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed reading configuration file: %w", err)
	}

	if err := yaml.Unmarshal(configBytes, cfg); err != nil {
		return fmt.Errorf("failed unmarshalling configuration file: %w", err)
	}

	if cfg.LogLevel == nil {
		cfg.LogLevel = new(string)
		*cfg.LogLevel = "info"
	}
	if cfg.DeviceName == nil {
		cfg.DeviceName = new(string)
		*cfg.DeviceName = "go-librespot"
	}
	if cfg.DeviceType == nil {
		cfg.DeviceType = new(string)
		*cfg.DeviceType = "computer"
	}
	if cfg.AudioDevice == nil {
		cfg.AudioDevice = new(string)
		*cfg.AudioDevice = "default"
	}
	if cfg.Bitrate == nil {
		cfg.Bitrate = new(int)
		*cfg.Bitrate = 160
	}
	if cfg.VolumeSteps == nil {
		cfg.VolumeSteps = new(uint32)
		*cfg.VolumeSteps = 100
	}
	if cfg.NormalisationPregain == nil {
		cfg.NormalisationPregain = new(float32)
		*cfg.NormalisationPregain = 0
	}

	return nil
}

func main() {
	rand.Seed(uint64(time.Now().UnixNano()))

	var cfg Config
	if err := loadConfig(&cfg); err != nil {
		log.WithError(err).Fatal("failed loading config")
	}

	// parse and set log level
	logLevel, err := log.ParseLevel(*cfg.LogLevel)
	if err != nil {
		log.WithError(err).Fatalf("invalid log level: %s", *cfg.LogLevel)
	} else {
		log.SetLevel(logLevel)
	}

	// create new app
	app, err := NewApp(&cfg)
	if err != nil {
		log.WithError(err).Fatal("failed creating app")
	}

	// create api server if needed
	if cfg.ServerPort != nil {
		app.server, err = NewApiServer(*cfg.ServerPort)
		if err != nil {
			log.WithError(err).Fatal("failed creating api server")
		}
	} else {
		app.server, _ = NewStubApiServer()
	}

	switch cfg.Credentials.Type {
	case "zeroconf":
		if err := app.Zeroconf(); err != nil {
			log.WithError(err).Fatal("failed running zeroconf")
		}
	case "user_pass":
		if err := app.UserPass(cfg.Credentials.UserPass.Username, cfg.Credentials.UserPass.Password); err != nil {
			log.WithError(err).Fatal("failed running with username and password")
		}
	case "spotify_token":
		if err := app.SpotifyToken(cfg.Credentials.SpotifyToken.Username, cfg.Credentials.SpotifyToken.AccessToken); err != nil {
			log.WithError(err).Fatal("failed running with username and spotify token")
		}
	default:
		log.Fatalf("unknown credentials: %s", cfg.Credentials.Type)
	}
}
