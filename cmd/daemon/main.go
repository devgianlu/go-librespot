package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/daemon"
	"github.com/devgianlu/go-librespot/mpris"
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/rand"
)

func main() {
	rand.Seed(uint64(time.Now().UnixNano()))

	var cfg cliConfig
	if err := loadCLIConfig(&cfg); err != nil {
		if errors.Is(err, errAlreadyRunning) {
			_, _ = fmt.Fprintln(os.Stderr, "could not start:", err)
			os.Exit(1)
		}
		log.WithError(err).Fatal("failed loading config")
	}

	logger := log.StandardLogger()
	logger.SetFormatter(&log.TextFormatter{
		DisableTimestamp: cfg.LogDisableTimestamp,
	})
	log.SetLevel(cfg.LogLevel)

	logEntry := &LogrusAdapter{Log: log.NewEntry(logger)}

	logger.Infof("running go-librespot %s", librespot.VersionNumberString())

	store := NewFileStateStore(
		filepath.Join(cfg.ConfigDir, "state.json"),
		filepath.Join(cfg.ConfigDir, "credentials.json"),
		logEntry,
	)

	var apiServer daemon.ApiServer
	if cfg.Server.Enabled {
		var err error
		apiServer, err = daemon.NewApiServer(logEntry, cfg.Server.Address, cfg.Server.Port, cfg.Server.AllowOrigin, cfg.Server.CertFile, cfg.Server.KeyFile)
		if err != nil {
			logger.WithError(err).Fatal("failed creating api server")
		}
	}

	var mediaPlayer mpris.Server
	if cfg.MprisEnabled {
		var err error
		mediaPlayer, err = mpris.NewServer(logEntry)
		if err != nil {
			logger.WithError(err).Fatal("failed creating mpris server")
		}
	}

	app, err := daemon.New(&daemon.Options{
		Logger:      logEntry,
		Config:      cfg.toDaemonConfig(),
		StateStore:  store,
		APIServer:   apiServer,
		MediaPlayer: mediaPlayer,
	})
	if err != nil {
		logger.WithError(err).Fatal("failed creating app")
	}
	defer func() { _ = app.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.WithError(err).Fatal("daemon exited with error")
	}
}
