package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/devgianlu/go-librespot/audiotagger"
)

func main() {
	watchDir := getenv("WATCH_DIR", "/music")
	scanInterval := getDuration("SCAN_INTERVAL", 3*time.Second)

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := audiotagger.Run(ctx, watchDir, scanInterval); err != nil {
		log.Fatal(err)
	}
}

func getenv(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))

	if value == "" {
		return fallback
	}

	return value
}

func getDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))

	if value == "" {
		return fallback
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		log.Printf(
			"invalid %s=%q, using %s",
			name,
			value,
			fallback,
		)

		return fallback
	}

	return duration
}
