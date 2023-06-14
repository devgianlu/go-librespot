package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	log "github.com/sirupsen/logrus"
	"go-librespot/ap"
	"go-librespot/apresolve"
)

type App struct {
	resolver *apresolve.ApResolver

	deviceId string

	ap *ap.AccessPoint
}

func NewApp() (app *App, err error) {
	app = &App{}
	app.resolver = apresolve.NewApResolver()

	// FIXME: make device id persistent
	deviceIdBytes := make([]byte, 20)
	_, _ = rand.Read(deviceIdBytes)
	app.deviceId = hex.EncodeToString(deviceIdBytes)

	return app, nil
}

func (app *App) Connect() (err error) {
	apAddr, err := app.resolver.GetAccessPoint()
	if err != nil {
		return fmt.Errorf("failed getting accesspoint from resolver: %w", err)
	}

	app.ap, err = ap.NewAccessPoint(apAddr)
	if err != nil {
		return fmt.Errorf("failed initializing accesspoint: %w", err)
	}

	if err = app.ap.Connect(); err != nil {
		return fmt.Errorf("failed connecting to accesspoint: %w", err)
	}

	if err = app.ap.Authenticate("xxxx", "xxxx", app.deviceId); err != nil {
		return fmt.Errorf("failed authenticating with accesspoint: %w", err)
	}

	return nil
}

func (app *App) handleAccesspointPacket(pktType ap.PacketType, payload []byte) error {
	switch pktType {
	case ap.PacketTypeProductInfo:
		var prod ProductInfo
		if err := xml.Unmarshal(payload, &prod); err != nil {
			return fmt.Errorf("failed umarshalling ProductInfo: %w", err)
		}

		// TODO: we may need this
		return nil
	default:
		return nil
	}
}

func (app *App) Run() {
	apRecv := app.ap.Receive(ap.PacketTypeProductInfo)

	for {
		select {
		case pkt := <-apRecv:
			if err := app.handleAccesspointPacket(pkt.Type, pkt.Payload); err != nil {
				log.WithError(err).Warn("failed handling accesspoint packet")
			}
		}
	}
}

func main() {
	app, err := NewApp()
	if err != nil {
		log.WithError(err).Fatal("failed creating app")
	}

	if err := app.Connect(); err != nil {
		log.WithError(err).Fatal("failed connecting app")
	}

	app.Run()
}
