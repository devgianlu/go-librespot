package session

import (
	"context"
	"encoding/hex"
	"fmt"
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/apresolve"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/login5"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	credentialspb "github.com/devgianlu/go-librespot/proto/spotify/login5/v3/credentials"
	"github.com/devgianlu/go-librespot/spclient"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	spotifyoauth2 "golang.org/x/oauth2/spotify"
)

type Session struct {
	deviceType  devicespb.DeviceType
	deviceId    string
	clientToken string

	resolver *apresolve.ApResolver
	login5   *login5.Login5

	ap       *ap.Accesspoint
	sp       *spclient.Spclient
	dealer   *dealer.Dealer
	audioKey *audio.KeyProvider
}

func NewSessionFromOptions(opts *Options) (*Session, error) {
	// validate device type
	if opts.DeviceType == devicespb.DeviceType_UNKNOWN {
		return nil, fmt.Errorf("missing device type")
	}

	// validate device id
	if deviceId, err := hex.DecodeString(opts.DeviceId); err != nil {
		return nil, fmt.Errorf("invalid device id: %w", err)
	} else if len(deviceId) != 20 {
		return nil, fmt.Errorf("invalid device id length: %s", opts.DeviceId)
	}

	s := Session{
		deviceType: opts.DeviceType,
		deviceId:   opts.DeviceId,
	}

	// use provided client token or retrieve a new one
	if len(opts.ClientToken) == 0 {
		var err error
		s.clientToken, err = retrieveClientToken(s.deviceId)
		if err != nil {
			return nil, fmt.Errorf("failed obtaining client token: %w", err)
		}
	} else {
		s.clientToken = opts.ClientToken
	}

	// use provided resolver or create a new one
	if opts.Resolver != nil {
		s.resolver = opts.Resolver
	} else {
		s.resolver = apresolve.NewApResolver()
	}

	// create new login5.Login5
	s.login5 = login5.NewLogin5(s.deviceId, s.clientToken)

	// connect to the accesspoint
	if apAddr, err := s.resolver.GetAccesspoint(); err != nil {
		return nil, fmt.Errorf("failed getting accesspoint from resolver: %w", err)
	} else if s.ap, err = ap.NewAccesspoint(apAddr, s.deviceId); err != nil {
		return nil, fmt.Errorf("failed initializing accesspoint: %w", err)
	}

	// authenticate with the accesspoint using the proper credentials
	switch creds := opts.Credentials.(type) {
	case StoredCredentials:
		if err := s.ap.ConnectStored(creds.Username, creds.Data); err != nil {
			return nil, fmt.Errorf("failed authenticating accesspoint with stored credentials: %w", err)
		}
	case InteractiveCredentials:
		ctx := context.Background()
		serverCtx, serverCancel := context.WithCancel(ctx)

		callbackPort, codeCh, err := NewOAuth2Server(serverCtx, creds.CallbackPort)
		if err != nil {
			serverCancel()
			return nil, fmt.Errorf("failed initializing oauth2 server: %w", err)
		}

		oauthConf := &oauth2.Config{
			ClientID:    librespot.ClientIdHex,
			RedirectURL: fmt.Sprintf("http://127.0.0.1:%d/login", callbackPort),
			Scopes: []string{
				"app-remote-control",
				"playlist-modify",
				"playlist-modify-private",
				"playlist-modify-public",
				"playlist-read",
				"playlist-read-collaborative",
				"playlist-read-private",
				"streaming",
				"ugc-image-upload",
				"user-follow-modify",
				"user-follow-read",
				"user-library-modify",
				"user-library-read",
				"user-modify",
				"user-modify-playback-state",
				"user-modify-private",
				"user-personalized",
				"user-read-birthdate",
				"user-read-currently-playing",
				"user-read-email",
				"user-read-play-history",
				"user-read-playback-position",
				"user-read-playback-state",
				"user-read-private",
				"user-read-recently-played",
				"user-top-read",
			},
			Endpoint: spotifyoauth2.Endpoint,
		}

		verifier := oauth2.GenerateVerifier()
		url := oauthConf.AuthCodeURL("", oauth2.S256ChallengeOption(verifier))
		log.Infof("to complete authentication visit the following link: %s", url)

		code := <-codeCh
		serverCancel()

		token, err := oauthConf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("failed exchanging oauth2 code: %w", err)
		}

		if err := s.ap.ConnectSpotifyToken(token.Extra("username").(string), token.AccessToken); err != nil {
			return nil, fmt.Errorf("failed authenticating accesspoint interactively: %w", err)
		}
	case SpotifyTokenCredentials:
		if err := s.ap.ConnectSpotifyToken(creds.Username, creds.Token); err != nil {
			return nil, fmt.Errorf("failed authenticating accesspoint with username and spotify token: %w", err)
		}
	case BlobCredentials:
		if err := s.ap.ConnectBlob(creds.Username, creds.Blob); err != nil {
			return nil, fmt.Errorf("failed authenticating accesspoint with blob: %w", err)
		}
	default:
		panic("unknown credentials")
	}

	// authenticate with login5
	if err := s.login5.Login(&credentialspb.StoredCredential{
		Username: s.ap.Username(),
		Data:     s.ap.StoredCredentials(),
	}); err != nil {
		return nil, fmt.Errorf("failed authenticating with login5: %w", err)
	}

	// initialize spclient
	if spAddr, err := s.resolver.GetSpclient(); err != nil {
		return nil, fmt.Errorf("failed getting spclient from resolver: %w", err)
	} else if s.sp, err = spclient.NewSpclient(spAddr, s.login5.AccessToken(), s.deviceId, s.clientToken); err != nil {
		return nil, fmt.Errorf("failed initializing spclient: %w", err)
	}

	// initialize dealer
	if dealerAddr, err := s.resolver.GetDealer(); err != nil {
		return nil, fmt.Errorf("failed getting dealer from resolver: %w", err)
	} else if s.dealer, err = dealer.NewDealer(dealerAddr, s.login5.AccessToken()); err != nil {
		return nil, fmt.Errorf("failed connecting to dealer: %w", err)
	}

	// init audio key provider
	s.audioKey = audio.NewAudioKeyProvider(s.ap)

	return &s, nil
}

func (s *Session) Close() {
	s.audioKey.Close()
	s.dealer.Close()
	s.ap.Close()
}
