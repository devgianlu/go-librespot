package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/ilyakaznacheev/cleanenv"
	log "github.com/sirupsen/logrus"
	"go-librespot/apresolve"
	devicespb "go-librespot/proto/spotify/connectstate/devices"
	"go-librespot/zeroconf"
	"sync"
)

type App struct {
	resolver *apresolve.ApResolver

	deviceName  string
	deviceId    string
	deviceType  devicespb.DeviceType
	clientToken string

	sess     *Session
	sessLock sync.Mutex
}

func NewApp(cfg *Config) (app *App, err error) {
	app = &App{deviceName: cfg.DeviceName}
	app.resolver = apresolve.NewApResolver()

	app.deviceType = devicespb.DeviceType_COMPUTER

	// FIXME: make device id persistent
	deviceIdBytes := make([]byte, 20)
	_, _ = rand.Read(deviceIdBytes)
	app.deviceId = hex.EncodeToString(deviceIdBytes)

	// FIXME: make client token persistent
	app.clientToken, err = retrieveClientToken(app.deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed obtaining client token: %w", err)
	}

	return app, nil
}

func (app *App) newSession(creds SessionCredentials) (*Session, error) {
	// connect new session
	sess := &Session{app: app}
	if err := sess.Connect(creds); err != nil {
		return nil, err
	}

	app.sessLock.Lock()
	defer app.sessLock.Unlock()

	// disconnect previous session
	if app.sess != nil {
		app.sess.Close()
	}

	// update current session
	app.sess = sess
	return sess, nil
}

func (app *App) Zeroconf() error {
	// pre fetch resolver endpoints
	if err := app.resolver.FetchAll(); err != nil {
		return fmt.Errorf("failed getting endpoints from resolver: %w", err)
	}

	// start zeroconf server and dispatch
	z, err := zeroconf.NewZeroconf(app.deviceName, app.deviceId, app.deviceType)
	if err != nil {
		return fmt.Errorf("failed initializing zeroconf: %w", err)
	}

	return z.Serve(func(req zeroconf.NewUserRequest) bool {
		sess, err := app.newSession(SessionBlobCredentials{
			Username: req.Username,
			Blob:     req.AuthBlob,
		})
		if err != nil {
			log.WithError(err).Errorf("failed creating new session for %s from %s", req.Username, req.DeviceName)
			return false
		}

		go sess.Run()
		return true
	})
}

func (app *App) UserPass(username, password string) error {
	sess, err := app.newSession(SessionUserPassCredentials{username, password})
	if err != nil {
		return err
	}

	sess.Run()
	return nil
}

type Config struct {
	LogLevel   string `yaml:"log_level" env:"LOG_LEVEL" env-default:"info"`
	DeviceName string `yaml:"device_name" env:"DEVICE_NAME" env-default:"go-librespot"`
	AuthMethod string `yaml:"auth_method" env:"AUTH_METHOD" env-default:"zeroconf"`
	Username   string `yaml:"username" env:"USERNAME" env-default:""`
	Password   string `yaml:"password" env:"PASSWORD" env-default:""`
}

func main() {
	var cfg Config
	if err := cleanenv.ReadConfig("config.yml", &cfg); err != nil {
		log.WithError(err).Fatal("failed reading configuration")
	}

	// parse and set log level
	logLevel, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		log.WithError(err).Fatalf("invalid log level: %s", cfg.LogLevel)
	} else {
		log.SetLevel(logLevel)
	}

	// create new app
	app, err := NewApp(&cfg)
	if err != nil {
		log.WithError(err).Fatal("failed creating app")
	}

	switch cfg.AuthMethod {
	case "zeroconf":
		if err := app.Zeroconf(); err != nil {
			log.WithError(err).Fatal("failed running zeroconf")
		}
	case "password":
		if err := app.UserPass(cfg.Username, cfg.Password); err != nil {
			log.WithError(err).Fatal("failed running with username and password")
		}
	default:
		log.Fatalf("unknown auth method: %s", cfg.AuthMethod)
	}
}
