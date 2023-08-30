package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ilyakaznacheev/cleanenv"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/apresolve"
	"go-librespot/player"
	devicespb "go-librespot/proto/spotify/connectstate/devices"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"go-librespot/zeroconf"
	"os"
	"strings"
	"sync"
)

type App struct {
	cfg *Config

	resolver *apresolve.ApResolver

	deviceId    string
	deviceType  devicespb.DeviceType
	clientToken string

	sess     *Session
	sessLock sync.Mutex

	server *ApiServer
}

func parseDeviceType(val string) (devicespb.DeviceType, error) {
	valEnum, ok := devicespb.DeviceType_value[strings.ToUpper(val)]
	if !ok {
		return 0, fmt.Errorf("invalid device type: %s", val)
	}

	return devicespb.DeviceType(valEnum), nil
}

func NewApp(cfg *Config) (app *App, err error) {
	app = &App{cfg: cfg}
	app.resolver = apresolve.NewApResolver()

	app.deviceType, err = parseDeviceType(cfg.DeviceType)
	if err != nil {
		return nil, err
	}

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

func (app *App) handleApiRequest(req ApiRequest, sess *Session) (any, error) {
	switch req.Type {
	case ApiRequestTypeStatus:
		resp := &ApiResponseStatus{
			Username:    sess.ap.Username(),
			DeviceId:    sess.app.deviceId,
			DeviceType:  sess.app.deviceType.String(),
			DeviceName:  sess.app.cfg.DeviceName,
			VolumeSteps: sess.app.cfg.VolumeSteps,
		}

		var trackPosition int64
		sess.withState(func(s *State) {
			resp.Volume = s.deviceInfo.Volume
			resp.RepeatContext = s.playerState.Options.RepeatingContext
			resp.RepeatTrack = s.playerState.Options.RepeatingTrack
			resp.ShuffleContext = s.playerState.Options.ShufflingContext
			resp.Paused = s.playerState.IsPaused
			resp.Buffering = s.playerState.IsBuffering
			resp.PlayOrigin = s.playerState.PlayOrigin.FeatureIdentifier

			trackPosition = s.trackPosition()
		})

		if sess.stream != nil && sess.prodInfo != nil {
			resp.Track = NewApiResponseStatusTrack(sess.stream.Track, sess.prodInfo, int(trackPosition))
		}

		return resp, nil
	case ApiRequestTypeResume:
		_ = sess.play()
		return nil, nil
	case ApiRequestTypePause:
		_ = sess.pause()
		return nil, nil
	case ApiRequestTypeSeek:
		_ = sess.seek(req.Data.(int64))
		return nil, nil
	case ApiRequestTypePrev:
		_ = sess.skipPrev()
		return nil, nil
	case ApiRequestTypeNext:
		_ = sess.skipNext()
		return nil, nil
	case ApiRequestTypePlay:
		data := req.Data.(ApiRequestDataPlay)
		ctx, err := sess.sp.ContextResolve(data.Uri)
		if err != nil {
			return nil, fmt.Errorf("failed resolving context: %w", err)
		}

		sess.withState(func(s *State) {
			s.isActive = true

			s.playerState.Suppressions = &connectpb.Suppressions{}
			s.playerState.PlayOrigin = &connectpb.PlayOrigin{
				FeatureIdentifier: "go-librespot",
				FeatureVersion:    librespot.VersionNumberString(),
			}
		})

		if err := sess.loadContext(ctx, func(track *connectpb.ContextTrack) bool {
			return len(data.SkipToUri) != 0 && data.SkipToUri == track.Uri
		}, data.Paused); err != nil {
			return nil, fmt.Errorf("failed loading context: %w", err)
		}

		return nil, nil
	case ApiRequestTypeGetVolume:
		resp := &ApiResponseVolume{
			Max: sess.app.cfg.VolumeSteps,
		}

		sess.withState(func(s *State) {
			resp.Value = s.deviceInfo.Volume * sess.app.cfg.VolumeSteps / player.MaxStateVolume
		})

		return resp, nil
	case ApiRequestTypeSetVolume:
		vol := req.Data.(uint32)
		sess.updateVolume(vol * player.MaxStateVolume / sess.app.cfg.VolumeSteps)
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown request type: %s", req.Type)
	}
}

func (app *App) Zeroconf() error {
	// pre fetch resolver endpoints
	if err := app.resolver.FetchAll(); err != nil {
		return fmt.Errorf("failed getting endpoints from resolver: %w", err)
	}

	// start zeroconf server and dispatch
	z, err := zeroconf.NewZeroconf(app.cfg.DeviceName, app.deviceId, app.deviceType)
	if err != nil {
		return fmt.Errorf("failed initializing zeroconf: %w", err)
	}

	// TODO: unset this when logging out
	var currentSession *Session

	go func() {
		for {
			select {
			case req := <-app.server.Receive():
				if currentSession == nil {
					req.Reply(nil, ErrNoSession)
					break
				}

				data, err := app.handleApiRequest(req, currentSession)
				req.Reply(data, err)
			}
		}
	}()

	return z.Serve(func(req zeroconf.NewUserRequest) bool {
		sess, err := app.newSession(SessionBlobCredentials{
			Username: req.Username,
			Blob:     req.AuthBlob,
		})
		if err != nil {
			log.WithError(err).Errorf("failed creating new session for %s from %s", req.Username, req.DeviceName)
			return false
		}

		currentSession = sess
		go sess.Run()
		return true
	})
}

type storedCredentialsFile struct {
	Username string `json:"username"`
	Data     []byte `json:"data"`
}

func (app *App) SpotifyToken(username, token string) error {
	sess, err := app.newSession(SessionSpotifyTokenCredentials{username, token})
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case req := <-app.server.Receive():
				data, err := app.handleApiRequest(req, sess)
				req.Reply(data, err)
			}
		}
	}()

	sess.Run()
	return nil
}

func (app *App) UserPass(username, password string) (err error) {
	var storedCredentials []byte
	if content, err := os.ReadFile("credentials.json"); err == nil {
		var file storedCredentialsFile
		if err := json.Unmarshal(content, &file); err != nil {
			return fmt.Errorf("failed unmarshalling stored credentials file: %w", err)
		}

		if file.Username == username {
			storedCredentials = file.Data
		}
	}

	var sess *Session
	if len(storedCredentials) > 0 {
		sess, err = app.newSession(SessionStoredCredentials{username, storedCredentials})
		if err != nil {
			return err
		}
	} else {
		sess, err = app.newSession(SessionUserPassCredentials{username, password})
		if err != nil {
			return err
		}

		if content, err := json.Marshal(&storedCredentialsFile{
			Username: sess.ap.Username(),
			Data:     sess.ap.StoredCredentials(),
		}); err != nil {
			return fmt.Errorf("failed marshalling stored credentials: %w", err)
		} else if err := os.WriteFile("credentials.json", content, 0600); err != nil {
			return fmt.Errorf("failed writing stored credentials file: %w", err)
		}
	}

	go func() {
		for {
			select {
			case req := <-app.server.Receive():
				data, err := app.handleApiRequest(req, sess)
				req.Reply(data, err)
			}
		}
	}()

	sess.Run()
	return nil
}

type Config struct {
	LogLevel    string `yaml:"log_level" env:"LOG_LEVEL" env-default:"info"`
	DeviceName  string `yaml:"device_name" env:"DEVICE_NAME" env-default:"go-librespot"`
	DeviceType  string `yaml:"device_type" env:"DEVICE_TYPE" env-default:"computer"`
	ServerPort  int    `yaml:"server_port" env:"SERVER_PORT" env-default:"0"`
	AudioDevice string `yaml:"audio_device" env:"AUDIO_DEVICE" env-default:""`
	AuthMethod  string `yaml:"auth_method" env:"AUTH_METHOD" env-default:"zeroconf"`
	Username    string `yaml:"username" env:"USERNAME" env-default:""`
	Password    string `yaml:"password" env:"PASSWORD" env-default:""`
	Token       string `yaml:"token" env:"TOKEN" env-default:""`
	Bitrate     int    `yaml:"bitrate" env:"BITRATE" env-default:"160"`
	VolumeSteps uint32 `yaml:"volume_steps" env:"VOLUME_STEPS" env-default:"100"`
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

	// create api server if needed
	if cfg.ServerPort != 0 {
		app.server, err = NewApiServer(cfg.ServerPort)
		if err != nil {
			log.WithError(err).Fatal("failed creating api server")
		}
	} else {
		app.server, _ = NewStubApiServer()
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
	case "spotify_token":
		if err := app.SpotifyToken(cfg.Username, cfg.Token); err != nil {
			log.WithError(err).Fatal("failed running with username and spotify token")
		}
	default:
		log.Fatalf("unknown auth method: %s", cfg.AuthMethod)
	}
}
