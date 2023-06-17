package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	log "github.com/sirupsen/logrus"
	"go-librespot/ap"
	"go-librespot/apresolve"
	"go-librespot/dealer"
	"go-librespot/login5"
	credentialspb "go-librespot/proto/spotify/login5/v3/credentials"
	"go-librespot/spclient"
)

type App struct {
	resolver *apresolve.ApResolver
	login5   *login5.Login5

	deviceId    string
	clientToken string

	ap     *ap.Accesspoint
	sp     *spclient.Spclient
	dealer *dealer.Dealer
}

func NewApp() (app *App, err error) {
	app = &App{}
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

	app.login5 = login5.NewLogin5(app.deviceId, app.clientToken)

	return app, nil
}

func (app *App) Connect() (err error) {
	// connect and authenticate to the accesspoint
	apAddr, err := app.resolver.GetAccesspoint()
	if err != nil {
		return fmt.Errorf("failed getting accesspoint from resolver: %w", err)
	}

	app.ap, err = ap.NewAccesspoint(apAddr, app.deviceId)
	if err != nil {
		return fmt.Errorf("failed initializing accesspoint: %w", err)
	}

	if err = app.ap.Connect(); err != nil {
		return fmt.Errorf("failed connecting to accesspoint: %w", err)
	}

	if err = app.ap.Authenticate("xxxx", "xxxx"); err != nil {
		return fmt.Errorf("failed authenticating with accesspoint: %w", err)
	}

	// authenticate with login5 and get token
	if err = app.login5.Login(&credentialspb.StoredCredential{
		Username: app.ap.Username(),
		Data:     app.ap.StoredCredentials(),
	}); err != nil {
		return fmt.Errorf("failed authenticating with login5: %w", err)
	}

	// initialize spclient
	spAddr, err := app.resolver.GetSpclient()
	if err != nil {
		return fmt.Errorf("failed getting spclient from resolver: %w", err)
	}

	app.sp, err = spclient.NewSpclient(spAddr, app.clientToken)
	if err != nil {
		return fmt.Errorf("failed initializing spclient: %w", err)
	}

	// initialize dealer
	dealerAddr, err := app.resolver.GetDealer()
	if err != nil {
		return fmt.Errorf("failed getting dealer from resolver: %w", err)
	}

	app.dealer, err = dealer.NewDealer(dealerAddr, app.login5.AccessToken())
	if err != nil {
		return fmt.Errorf("failed initializing dealer: %w", err)
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
	log.SetLevel(log.TraceLevel)

	app, err := NewApp()
	if err != nil {
		log.WithError(err).Fatal("failed creating app")
	}

	if err := app.Connect(); err != nil {
		log.WithError(err).Fatal("failed connecting app")
	}

	app.Run()
}
