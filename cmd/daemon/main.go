package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/devgianlu/go-librespot/apresolve"
	"github.com/devgianlu/go-librespot/output"
	"github.com/devgianlu/go-librespot/player"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/zeroconf"
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/rand"
	"gopkg.in/yaml.v3"
	"math"
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

	server   *ApiServer
	logoutCh chan *AppPlayer
}

func parseDeviceType(val string) (devicespb.DeviceType, error) {
	valEnum, ok := devicespb.DeviceType_value[strings.ToUpper(val)]
	if !ok {
		return 0, fmt.Errorf("invalid device type: %s", val)
	}

	return devicespb.DeviceType(valEnum), nil
}

func NewApp(cfg *Config) (app *App, err error) {
	app = &App{cfg: cfg, logoutCh: make(chan *AppPlayer)}

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
		app:                  app,
		stop:                 make(chan struct{}, 1),
		logout:               app.logoutCh,
		countryCode:          new(string),
		externalVolumeUpdate: output.NewRingBuffer[float32](1),
	}

	// start a dummy timer for prefetching next media
	appPlayer.prefetchTimer = time.AfterFunc(time.Duration(math.MaxInt64), appPlayer.prefetchNext)

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
		appPlayer.sess.Spclient(), appPlayer.sess.AudioKey(),
		!app.cfg.NormalisationDisabled, *app.cfg.NormalisationPregain,
		appPlayer.countryCode, *app.cfg.AudioDevice, *app.cfg.MixerDevice, *app.cfg.MixerControlName,
		*app.cfg.VolumeSteps, app.cfg.ExternalVolume, appPlayer.externalVolumeUpdate,
	); err != nil {
		return nil, fmt.Errorf("failed initializing player: %w", err)
	}

	// only update the "spotify volume", when external volume is enabled or a mixer is defined
	// try to keep synchronized with the device volume
	if app.cfg.ExternalVolume || len(*app.cfg.MixerDevice) > 0 {
		// listen on external volume changes (for example the alsa driver)
		go func() {
			for {
				v, err := appPlayer.externalVolumeUpdate.GetWait()
				if errors.Is(err, output.ErrBufferClosed) {
					break
				}

				appPlayer.updateVolume(uint32(v * player.MaxStateVolume))

				// prevent "too many requests"
				time.Sleep(2 * time.Second)
			}
		}()
	}

	return appPlayer, nil
}

func (app *App) Zeroconf() error {
	return app.withAppPlayer(func() (*AppPlayer, error) { return nil, nil })
}

func (app *App) SpotifyToken(username, token string) error {
	return app.withCredentials(session.SpotifyTokenCredentials{Username: username, Token: token})
}

func (app *App) Interactive(callbackPort int) error {
	return app.withCredentials(session.InteractiveCredentials{CallbackPort: callbackPort})
}

type storedCredentialsFile struct {
	Username string `json:"username"`
	Data     []byte `json:"data"`
}

func (app *App) withCredentials(creds any) (err error) {
	var storedUsername string
	var storedCredentials []byte
	if content, err := os.ReadFile(app.cfg.CredentialsPath); err == nil {
		var file storedCredentialsFile
		if err := json.Unmarshal(content, &file); err != nil {
			return fmt.Errorf("failed unmarshalling stored credentials file: %w", err)
		}

		storedUsername = file.Username
		storedCredentials = file.Data
		log.Debugf("stored credentials found for %s", file.Username)
	} else {
		log.Debugf("stored credentials not found")
	}

	return app.withAppPlayer(func() (*AppPlayer, error) {
		if len(storedCredentials) > 0 {
			return app.newAppPlayer(session.StoredCredentials{Username: storedUsername, Data: storedCredentials})
		} else {
			appPlayer, err := app.newAppPlayer(creds)
			if err != nil {
				return nil, err
			}

			// store credentials outside this context in case we get called again
			storedUsername = appPlayer.sess.Username()
			storedCredentials = appPlayer.sess.StoredCredentials()

			if content, err := json.Marshal(&storedCredentialsFile{
				Username: appPlayer.sess.Username(),
				Data:     appPlayer.sess.StoredCredentials(),
			}); err != nil {
				return nil, fmt.Errorf("failed marshalling stored credentials: %w", err)
			} else if err := os.WriteFile(app.cfg.CredentialsPath, content, 0600); err != nil {
				return nil, fmt.Errorf("failed writing stored credentials file: %w", err)
			}

			log.Debugf("stored credentials for %s", appPlayer.sess.Username())
			return appPlayer, nil
		}
	})
}

func (app *App) withAppPlayer(appPlayerFunc func() (*AppPlayer, error)) (err error) {
	// if zeroconf is disabled, there is not much we need to do
	if !app.cfg.ZeroconfEnabled {
		appPlayer, err := appPlayerFunc()
		if err != nil {
			return err
		} else if appPlayer == nil {
			panic("zeroconf is disabled and no credentials are present")
		}

		appPlayer.Run(app.server.Receive())
		return nil
	}

	// pre fetch resolver endpoints
	if err := app.resolver.FetchAll(); err != nil {
		return fmt.Errorf("failed getting endpoints from resolver: %w", err)
	}

	// start zeroconf server and dispatch
	z, err := zeroconf.NewZeroconf(*app.cfg.DeviceName, app.deviceId, app.deviceType)
	if err != nil {
		return fmt.Errorf("failed initializing zeroconf: %w", err)
	}

	var apiCh chan ApiRequest

	currentPlayer, err := appPlayerFunc()
	if err != nil {
		return err
	}

	// set current player to the provided one if we have it
	if currentPlayer != nil {
		log.Debugf("initializing zeroconf session, username: %s", currentPlayer.sess.Username())

		apiCh = make(chan ApiRequest)
		go currentPlayer.Run(apiCh)

		// let zeroconf know that we already have a user
		z.SetCurrentUser(currentPlayer.sess.Username())
	}

	// forward API requests to proper channel only if a session is present
	go func() {
		for {
			select {
			case req := <-app.server.Receive():
				if currentPlayer == nil {
					req.Reply(nil, ErrNoSession)
					break
				}

				// if we are here the channel must exist
				apiCh <- req
			}
		}
	}()

	// listen for logout events and unset session when that happens
	go func() {
		for {
			select {
			case p := <-app.logoutCh:
				// check that the logout request is for the current player
				if p != currentPlayer {
					continue
				}

				currentPlayer.Close()
				currentPlayer = nil

				// close the channel after setting the current session to nil
				close(apiCh)

				// restore the session if there is one.
				// we will restore the session even if it's for the same user, but it shouldn't be an issue
				newAppPlayer, err := appPlayerFunc()
				if err != nil {
					log.WithError(err).Errorf("failed restoring session after logout")
				} else if newAppPlayer == nil {
					// unset the zeroconf user
					z.SetCurrentUser("")
				} else {
					// first create the channel and then assign the current session
					apiCh = make(chan ApiRequest)
					currentPlayer = newAppPlayer

					go newAppPlayer.Run(apiCh)

					// let zeroconf know that we already have a user
					z.SetCurrentUser(newAppPlayer.sess.Username())

					log.Debugf("restored session after logout, username: %s", currentPlayer.sess.Username())
				}
			}
		}
	}()

	return z.Serve(func(req zeroconf.NewUserRequest) bool {
		if currentPlayer != nil {
			currentPlayer.Close()
			currentPlayer = nil

			// close the channel after setting the current session to nil
			close(apiCh)

			// no need to unset the zeroconf user here as the new one will overwrite it anyway
		}

		newAppPlayer, err := app.newAppPlayer(session.BlobCredentials{
			Username: req.Username,
			Blob:     req.AuthBlob,
		})
		if err != nil {
			log.WithError(err).Errorf("failed creating new session for %s from %s", req.Username, req.DeviceName)
			return false
		}

		// first create the channel and then assign the current session
		apiCh = make(chan ApiRequest)
		currentPlayer = newAppPlayer

		go newAppPlayer.Run(apiCh)
		return true
	})
}

type Config struct {
	ConfigPath      string `yaml:"-"`
	CredentialsPath string `yaml:"-"`

	LogLevel              *string  `yaml:"log_level"`
	DeviceId              *string  `yaml:"device_id"`
	DeviceName            *string  `yaml:"device_name"`
	DeviceType            *string  `yaml:"device_type"`
	ClientToken           *string  `yaml:"client_token"`
	AudioDevice           *string  `yaml:"audio_device"`
	MixerDevice           *string  `yaml:"mixer_device"`
	MixerControlName      *string  `yaml:"mixer_control_name"`
	Bitrate               *int     `yaml:"bitrate"`
	VolumeSteps           *uint32  `yaml:"volume_steps"`
	InitialVolume         *uint32  `yaml:"initial_volume"`
	NormalisationDisabled bool     `yaml:"normalisation_disabled"`
	NormalisationPregain  *float32 `yaml:"normalisation_pregain"`
	ExternalVolume        bool     `yaml:"external_volume"`
	ZeroconfEnabled       bool     `yaml:"zeroconf_enabled"`
	Server                struct {
		Enabled     bool   `yaml:"enabled"`
		Address     string `yaml:"address"`
		Port        int    `yaml:"port"`
		AllowOrigin string `yaml:"allow_origin"`
	} `yaml:"server"`
	Credentials struct {
		Type        string `yaml:"type"`
		Interactive struct {
			CallbackPort int `yaml:"callback_port"`
		} `yaml:"interactive"`
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
	if cfg.MixerDevice == nil {
		cfg.MixerDevice = new(string)
		*cfg.MixerDevice = ""
	}
	if cfg.MixerControlName == nil {
		cfg.MixerControlName = new(string)
		*cfg.MixerControlName = "Master"
	}
	if cfg.Bitrate == nil {
		cfg.Bitrate = new(int)
		*cfg.Bitrate = 160
	}
	if cfg.VolumeSteps == nil {
		cfg.VolumeSteps = new(uint32)
		*cfg.VolumeSteps = 100
	}
	if cfg.InitialVolume == nil {
		cfg.InitialVolume = new(uint32)
		*cfg.InitialVolume = 100
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
	if cfg.Server.Enabled {
		app.server, err = NewApiServer(cfg.Server.Address, cfg.Server.Port, cfg.Server.AllowOrigin)
		if err != nil {
			log.WithError(err).Fatal("failed creating api server")
		}
	} else {
		app.server, _ = NewStubApiServer()
	}

	switch cfg.Credentials.Type {
	case "zeroconf":
		// ensure zeroconf is enabled
		app.cfg.ZeroconfEnabled = true
		if err := app.Zeroconf(); err != nil {
			log.WithError(err).Fatal("failed running zeroconf")
		}
	case "interactive":
		if err := app.Interactive(cfg.Credentials.Interactive.CallbackPort); err != nil {
			log.WithError(err).Fatal("failed running with interactive auth")
		}
	case "spotify_token":
		if err := app.SpotifyToken(cfg.Credentials.SpotifyToken.Username, cfg.Credentials.SpotifyToken.AccessToken); err != nil {
			log.WithError(err).Fatal("failed running with username and spotify token")
		}
	default:
		log.Fatalf("unknown credentials: %s", cfg.Credentials.Type)
	}
}
