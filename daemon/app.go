package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/apresolve"
	"github.com/devgianlu/go-librespot/cache"
	"github.com/devgianlu/go-librespot/mpris"
	"github.com/devgianlu/go-librespot/player"
	"github.com/devgianlu/go-librespot/playplay"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/zeroconf"
	"golang.org/x/exp/rand"
)

type App struct {
	log librespot.Logger
	cfg *Config

	stateStore librespot.StateStore

	client *http.Client

	resolver *apresolve.ApResolver
	zeroconf *zeroconf.Zeroconf

	deviceId    string
	deviceType  devicespb.DeviceType
	clientToken string
	state       *librespot.AppState

	server   ApiServer
	mpris    mpris.Server
	logoutCh chan *AppPlayer

	audioCache *cache.Cache

	closed bool
}

func parseDeviceType(val string) (devicespb.DeviceType, error) {
	valEnum, ok := devicespb.DeviceType_value[strings.ToUpper(val)]
	if !ok {
		return 0, fmt.Errorf("invalid device type: %s", val)
	}

	return devicespb.DeviceType(valEnum), nil
}

func New(opts *Options) (*App, error) {
	if opts == nil {
		return nil, errors.New("daemon: Options is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("daemon: Options.Logger is required")
	}
	if opts.Config == nil {
		return nil, errors.New("daemon: Options.Config is required")
	}
	if opts.StateStore == nil {
		return nil, errors.New("daemon: Options.StateStore is required")
	}

	app := &App{
		log:        opts.Logger,
		cfg:        opts.Config,
		stateStore: opts.StateStore,
		logoutCh:   make(chan *AppPlayer),
		client:     &http.Client{Timeout: 30 * time.Second},
	}

	var err error
	app.deviceType, err = parseDeviceType(app.cfg.DeviceType)
	if err != nil {
		return nil, err
	}

	app.state, err = opts.StateStore.Load()
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}
	if app.state == nil {
		app.state = &librespot.AppState{}
	}

	app.resolver = apresolve.NewApResolver(app.log, app.client, app.cfg.PreferFirewallFriendlyPorts)

	if app.cfg.DeviceId != "" {
		app.deviceId = app.cfg.DeviceId
	} else if app.state.DeviceId != "" {
		app.deviceId = app.state.DeviceId
	} else {
		deviceIdBytes := make([]byte, 20)
		_, _ = rand.Read(deviceIdBytes)
		app.deviceId = hex.EncodeToString(deviceIdBytes)
		app.log.Infof("generated new device id: %s", app.deviceId)

		app.state.DeviceId = app.deviceId
		if err := app.persistState(); err != nil {
			return nil, err
		}
	}

	if app.cfg.ClientToken != "" {
		app.clientToken = app.cfg.ClientToken
	}

	if app.cfg.FlacEnabled && !playplay.Plugin.IsSupported() {
		return nil, fmt.Errorf("FLAC playback requires a PlapPlay implementation")
	}

	if opts.APIServer != nil {
		app.server = opts.APIServer
	} else {
		app.server, _ = NewStubApiServer(app.log)
	}

	if opts.MediaPlayer != nil {
		app.mpris = opts.MediaPlayer
	} else {
		app.mpris = mpris.DummyServer{}
	}

	if app.cfg.Cache.Enabled && app.cfg.Cache.Dir != "" {
		app.audioCache, err = cache.New(app.log, app.cfg.Cache.Dir, app.cfg.Cache.SizeLimit)
		if err != nil {
			return nil, fmt.Errorf("failed initializing audio cache: %w", err)
		}
	}

	return app, nil
}

func (app *App) SetDeviceName(name string) {
	if app.cfg.DeviceName == name {
		return
	}

	app.cfg.DeviceName = name

	if app.zeroconf != nil {
		app.zeroconf.SetDeviceName(name)
	}
}

// Run starts the daemon. It blocks until ctx is cancelled or an unrecoverable
// error occurs. The credential type configured in cfg.Credentials.Type
// determines which login flow is used.
func (app *App) Run(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			_ = app.Close()
		}
	}()

	switch app.cfg.Credentials.Type {
	case "zeroconf":
		// Zeroconf mode unconditionally needs zeroconf to be enabled.
		app.cfg.ZeroconfEnabled = true
		return app.runZeroconf(ctx)
	case "interactive":
		return app.runInteractive(ctx, app.cfg.Credentials.Interactive.CallbackPort)
	case "device_auth":
		return app.runDeviceAuth(ctx)
	case "spotify_token":
		return app.runSpotifyToken(ctx, app.cfg.Credentials.SpotifyToken.Username, app.cfg.Credentials.SpotifyToken.AccessToken)
	default:
		return fmt.Errorf("unknown credentials: %s", app.cfg.Credentials.Type)
	}
}

// Close releases resources held by the daemon. It is safe to call more than once.
func (app *App) Close() error {
	if app.closed {
		return nil
	}
	app.closed = true

	var errs []error
	if app.server != nil {
		if err := app.server.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if app.mpris != nil {
		if err := app.mpris.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if app.zeroconf != nil {
		app.zeroconf.Close()
	}

	return errors.Join(errs...)
}

func (app *App) persistState() error {
	if err := app.stateStore.Save(app.state); err != nil {
		return fmt.Errorf("persisting state: %w", err)
	}
	return nil
}

func (app *App) newAppPlayer(ctx context.Context, creds any) (_ *AppPlayer, err error) {
	playerCtx, playerCancel := context.WithCancel(ctx)

	appPlayer := &AppPlayer{
		app:             app,
		ctx:             playerCtx,
		cancel:          playerCancel,
		stop:            make(chan struct{}, 1),
		logout:          app.logoutCh,
		countryCode:     new(string),
		volumeUpdate:    make(chan float32, 1),
		playbackReadyCh: make(chan struct{}),
	}

	defer func() {
		if err != nil {
			playerCancel()
		}
	}()

	appPlayer.prefetchTimer = time.NewTimer(math.MaxInt64)
	appPlayer.prefetchTimer.Stop()

	appPlayer.sleepTimer = time.NewTimer(math.MaxInt64)
	appPlayer.sleepTimer.Stop()

	if appPlayer.sess, err = session.NewSessionFromOptions(ctx, &session.Options{
		Log:         app.log,
		DeviceType:  app.deviceType,
		DeviceId:    app.deviceId,
		ClientToken: app.clientToken,
		Resolver:    app.resolver,
		Client:      app.client,
		StateStore:  app.stateStore,
		Credentials: creds,
	}); err != nil {
		return nil, err
	}

	appPlayer.initState()

	if appPlayer.player, err = player.NewPlayer(&player.Options{
		Spclient: appPlayer.sess.Spclient(),
		AudioKey: appPlayer.sess.AudioKey(),
		Events:   appPlayer.sess.Events(),
		Log:      app.log,

		Cache: app.audioCache,

		FlacEnabled: app.cfg.FlacEnabled,

		NormalisationEnabled:      !app.cfg.NormalisationDisabled,
		NormalisationUseAlbumGain: app.cfg.NormalisationUseAlbumGain,
		NormalisationPregain:      app.cfg.NormalisationPregain,

		CrossfadeDuration: time.Duration(app.cfg.CrossfadeDuration) * time.Millisecond,

		CountryCode: appPlayer.countryCode,

		AudioBackend:              app.cfg.AudioBackend,
		AudioBackendRuntimeSocket: app.cfg.AudioBackendRuntimeSocket,
		AudioDevice:               app.cfg.AudioDevice,
		MixerDevice:               app.cfg.MixerDevice,
		MixerControlName:          app.cfg.MixerControlName,

		AudioBufferTime:  app.cfg.AudioBufferTime,
		AudioPeriodCount: app.cfg.AudioPeriodCount,

		ExternalVolume: app.cfg.ExternalVolume,
		VolumeUpdate:   appPlayer.volumeUpdate,

		AudioOutputPipe:              app.cfg.AudioOutputPipe,
		AudioOutputPipeFormat:        app.cfg.AudioOutputPipeFormat,
		AudioOutputPipeWaitForReader: app.cfg.AudioOutputPipeWaitForReader,
	},
	); err != nil {
		return nil, fmt.Errorf("failed initializing player: %w", err)
	}

	return appPlayer, nil
}

func (app *App) runZeroconf(ctx context.Context) error {
	return app.withAppPlayer(ctx, func(ctx context.Context) (*AppPlayer, error) {
		if app.cfg.Credentials.Zeroconf.PersistCredentials && len(app.state.Credentials.Data) > 0 {
			app.log.WithField("username", librespot.ObfuscateUsername(app.state.Credentials.Username)).
				Infof("loading previously persisted zeroconf credentials")
			return app.newAppPlayer(ctx, session.StoredCredentials{
				Username: app.state.Credentials.Username,
				Data:     app.state.Credentials.Data,
			})
		}

		return nil, nil
	})
}

func (app *App) runSpotifyToken(ctx context.Context, username, token string) error {
	return app.withCredentials(ctx, session.SpotifyTokenCredentials{Username: username, Token: token})
}

func (app *App) runInteractive(ctx context.Context, callbackPort int) error {
	return app.withCredentials(ctx, session.InteractiveCredentials{CallbackPort: callbackPort})
}

func (app *App) runDeviceAuth(ctx context.Context) error {
	return app.withCredentials(ctx, session.DeviceAuthCredentials{
		OnCode: func(code *session.DeviceAuthCode) {
			if code == nil {
				app.server.SetAuthCode(nil)
				return
			}

			app.server.SetAuthCode(&ApiDeviceAuth{
				Url:       code.VerificationUrl,
				Code:      code.UserCode,
				ExpiresAt: code.ExpiresAt,
			})
		},
	})
}

func (app *App) withCredentials(ctx context.Context, creds any) (err error) {
	return app.withAppPlayer(ctx, func(ctx context.Context) (*AppPlayer, error) {
		if len(app.state.Credentials.Data) > 0 {
			return app.newAppPlayer(ctx, session.StoredCredentials{
				Username: app.state.Credentials.Username,
				Data:     app.state.Credentials.Data,
			})
		} else {
			appPlayer, err := app.newAppPlayer(ctx, creds)
			if err != nil {
				return nil, err
			}

			app.state.Credentials.Username = appPlayer.sess.Username()
			app.state.Credentials.Data = appPlayer.sess.StoredCredentials()

			if err = app.persistState(); err != nil {
				return nil, err
			}

			app.log.WithField("username", librespot.ObfuscateUsername(appPlayer.sess.Username())).
				Debugf("stored credentials")
			return appPlayer, nil
		}
	})
}

// activeSession is the player currently serving API requests, bundled with the
// channel that feeds it so the two can only ever be swapped together.
type activeSession struct {
	player *AppPlayer
	apiCh  chan ApiRequest

	// done is closed once nothing will read apiCh again, either because the
	// session was replaced or because its Run returned. A sender must select
	// on it or risk blocking forever on a channel with no reader left.
	done     chan struct{}
	doneOnce sync.Once
}

func (s *activeSession) retire() {
	s.doneOnce.Do(func() { close(s.done) })
}

// forward hands req to the session's player. It reports false if the session
// went away first, in which case the request was not delivered and the caller
// still owes the client a reply.
func (s *activeSession) forward(ctx context.Context, req ApiRequest) bool {
	select {
	case s.apiCh <- req:
		return true
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (app *App) withAppPlayer(ctx context.Context, appPlayerFunc func(context.Context) (*AppPlayer, error)) (err error) {
	if !app.cfg.ZeroconfEnabled {
		appPlayer, err := appPlayerFunc(ctx)
		if err != nil {
			return err
		} else if appPlayer == nil {
			panic("zeroconf is disabled and no credentials are present")
		}

		appPlayer.Run(app.server.Receive(), app.mpris.Receive())
		return nil
	}

	if err := app.resolver.FetchAll(ctx); err != nil {
		return fmt.Errorf("failed getting endpoints from resolver: %w", err)
	}

	app.zeroconf, err = zeroconf.NewZeroconf(app.log, app.cfg.ZeroconfPort, app.cfg.DeviceName, app.deviceId, app.deviceType, app.cfg.ZeroconfInterfacesToAdvertise, app.cfg.ZeroconfBackend == "avahi")
	if err != nil {
		return fmt.Errorf("failed initializing zeroconf: %w", err)
	}

	// The session is swapped by the logout goroutine and by the zeroconf
	// callback while the API goroutine below is forwarding requests into it.
	// Keeping the player and its channel together behind one mutex-guarded
	// pointer means a forwarder always sees a consistent pair, instead of a
	// live player with the channel of the session that replaced it.
	var sessionMu sync.Mutex
	var current *activeSession

	// setSession installs newPlayer as the session serving API requests and
	// tears down whichever one it replaces; a nil newPlayer just tears down.
	setSession := func(newPlayer *AppPlayer) {
		sessionMu.Lock()
		prev := current
		if newPlayer == nil {
			current = nil
		} else {
			current = &activeSession{
				player: newPlayer,
				apiCh:  make(chan ApiRequest),
				done:   make(chan struct{}),
			}
		}
		next := current
		sessionMu.Unlock()

		if prev != nil {
			// Retiring the session rather than closing its channel: a request
			// may already be on its way into apiCh, and closing underneath
			// that send takes the whole daemon down with "send on closed
			// channel". The forwarder watches done instead and gives up.
			prev.retire()
			prev.player.Close()
		}

		if next != nil {
			go func() {
				next.player.Run(next.apiCh, app.mpris.Receive())

				// Run stopped by itself (it gives up when the dealer is
				// unreachable): nothing will read apiCh again, so release any
				// forwarder waiting on it rather than let it block forever.
				next.retire()

				// Drop it as the current session too, so requests get the
				// no-session treatment instead of a session that cannot
				// answer. Guarded because a replacement may already be in
				// place — this also runs on an ordinary teardown.
				sessionMu.Lock()
				if current == next {
					current = nil
				}
				sessionMu.Unlock()
			}()
		}
	}

	initialPlayer, err := appPlayerFunc(ctx)
	if err != nil {
		return err
	}

	if initialPlayer != nil {
		app.log.WithField("username", librespot.ObfuscateUsername(initialPlayer.sess.Username())).
			Debugf("initializing zeroconf session")

		setSession(initialPlayer)

		app.zeroconf.SetCurrentUser(initialPlayer.sess.Username())
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-app.server.Receive():
				sessionMu.Lock()
				sess := current
				sessionMu.Unlock()

				if sess == nil {
					switch req.Type {
					case ApiRequestTypeRoot:
						req.Reply(&ApiRoot{}, nil)
					case ApiRequestSetDeviceName:
						// The device name drives the zeroconf advertisement, which
						// runs independently of any player session, so handle it
						// even when no session is active.
						app.SetDeviceName(req.Data.(string))
						req.Reply(nil, nil)
					default:
						req.Reply(nil, ErrNoSession)
					}
					continue
				}

				if !sess.forward(ctx, req) {
					// The session went away while this request was in flight.
					req.Reply(nil, ErrNoSession)
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				setSession(nil)
				return
			case p := <-app.logoutCh:
				sessionMu.Lock()
				isCurrent := current != nil && current.player == p
				sessionMu.Unlock()

				if !isCurrent {
					continue
				}

				setSession(nil)

				newAppPlayer, err := appPlayerFunc(ctx)
				if err != nil {
					app.log.WithError(err).Errorf("failed restoring session after logout")

					app.zeroconf.SetCurrentUser("")
				} else if newAppPlayer == nil {
					app.zeroconf.SetCurrentUser("")
				} else {
					setSession(newAppPlayer)

					app.zeroconf.SetCurrentUser(newAppPlayer.sess.Username())

					app.log.WithField("username", librespot.ObfuscateUsername(newAppPlayer.sess.Username())).
						Debugf("restored session after logout")
				}
			}
		}
	}()

	return app.zeroconf.Serve(func(req zeroconf.NewUserRequest) bool {
		setSession(nil)

		newAppPlayer, err := app.newAppPlayer(ctx, session.BlobCredentials{
			Username: req.Username,
			Blob:     req.AuthBlob,
		})
		if err != nil {
			app.log.WithError(err).WithField("username", librespot.ObfuscateUsername(req.Username)).
				Errorf("failed creating new session from %s", req.DeviceName)
			return false
		}

		if app.cfg.Credentials.Zeroconf.PersistCredentials {
			app.state.Credentials.Username = newAppPlayer.sess.Username()
			app.state.Credentials.Data = newAppPlayer.sess.StoredCredentials()

			if err := app.persistState(); err != nil {
				app.log.WithError(err).Errorf("failed persisting zeroconf credentials")
			}

			app.log.WithField("username", librespot.ObfuscateUsername(newAppPlayer.sess.Username())).
				Debugf("persisted zeroconf credentials")
		}

		setSession(newAppPlayer)
		return true
	})
}
