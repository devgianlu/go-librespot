package main

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"go-librespot/ap"
)

type App struct {
	ap *ap.AccessPoint
}

func NewApp() (app *App, err error) {
	app = &App{}
	return app, nil
}

func (app *App) Connect() (err error) {
	// TODO: contact resolver to get accesspoint
	app.ap, err = ap.NewAccessPoint("ap-gew4.spotify.com", 4070)
	if err != nil {
		return fmt.Errorf("failed initializing accesspoint: %w", err)
	}

	if err = app.ap.Connect(); err != nil {
		return fmt.Errorf("failed connecting to accesspoint: %w", err)
	}

	if err = app.ap.Authenticate("xxxx", "xxxx"); err != nil {
		return fmt.Errorf("failed authenticating with accesspoint: %w", err)
	}

	return nil
}

func main() {
	app, err := NewApp()
	if err != nil {
		log.WithError(err).Fatal("failed creating app")
	}

	if err := app.Connect(); err != nil {
		log.WithError(err).Fatal("failed connecting app")
	}
}
