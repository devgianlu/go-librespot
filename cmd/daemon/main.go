package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	log "github.com/sirupsen/logrus"
	"go-librespot/apresolve"
	"go-librespot/zeroconf"
	"sync"
)

type App struct {
	resolver *apresolve.ApResolver

	deviceName  string
	deviceId    string
	clientToken string

	sess     *Session
	sessLock sync.Mutex
}

func NewApp(deviceName string) (app *App, err error) {
	app = &App{deviceName: deviceName}
	app.resolver = apresolve.NewApResolver()

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
	z, err := zeroconf.NewZeroconf(app.deviceName, app.deviceId)
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

const UseZeroconf = true
const AuthUsername = "xxxx"
const AuthPassword = "xxxx"

func main() {
	log.SetLevel(log.TraceLevel)

	app, err := NewApp("go-librespot test")
	if err != nil {
		log.WithError(err).Fatal("failed creating app")
	}

	if UseZeroconf {
		if err := app.Zeroconf(); err != nil {
			log.WithError(err).Fatal("failed running zeroconf")
		}
	} else {
		if err := app.UserPass(AuthUsername, AuthPassword); err != nil {
			log.WithError(err).Fatal("failed running with username and password")
		}
	}
}
