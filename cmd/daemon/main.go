package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/apresolve"
	"go-librespot/player"
	devicespb "go-librespot/proto/spotify/connectstate/devices"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"go-librespot/zeroconf"
	"gopkg.in/yaml.v3"
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
	sess := &Session{app: app, countryCode: new(string)}
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
			resp.Stopped = !s.playerState.IsPlaying
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
	return app.withReusableCredentials(SessionSpotifyTokenCredentials{username, token})
}

func (app *App) UserPass(username, password string) error {
	return app.withReusableCredentials(SessionUserPassCredentials{username, password})
}

func (app *App) withReusableCredentials(creds SessionCredentials) (err error) {
	var username string
	switch creds := creds.(type) {
	case SessionSpotifyTokenCredentials:
		username = creds.Username
	case SessionUserPassCredentials:
		username = creds.Username
	default:
		return fmt.Errorf("unsupported credentials for reuse")
	}

	var storedCredentials []byte
	if content, err := os.ReadFile(app.cfg.CredentialsPath); err == nil {
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
		sess, err = app.newSession(creds)
		if err != nil {
			return err
		}

		if content, err := json.Marshal(&storedCredentialsFile{
			Username: sess.ap.Username(),
			Data:     sess.ap.StoredCredentials(),
		}); err != nil {
			return fmt.Errorf("failed marshalling stored credentials: %w", err)
		} else if err := os.WriteFile(app.cfg.CredentialsPath, content, 0600); err != nil {
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
	ConfigPath      string `yaml:"-"`
	CredentialsPath string `yaml:"-"`

	LogLevel    string `yaml:"log_level"`
	DeviceName  string `yaml:"device_name"`
	DeviceType  string `yaml:"device_type"`
	ServerPort  int    `yaml:"server_port"`
	AudioDevice string `yaml:"audio_device"`
	Bitrate     int    `yaml:"bitrate"`
	VolumeSteps uint32 `yaml:"volume_steps"`
	Credentials struct {
		Type     string `yaml:"type"`
		UserPass struct {
			Username string `yaml:"username"`
			Password string `yaml:"password"`
		} `yaml:"user_pass"`
		SpotifyToken struct {
			Username    string `yaml:"username"`
			AccessToken string `yaml:"access_token"`
		} `yaml:"spotify_token"`
	} `yaml:"credentials"`
}

func loadConfig(cfg *Config) error {
	flag.StringVar(&cfg.ConfigPath, "config_path", "config.yml", "the configuration file path")
	flag.StringVar(&cfg.CredentialsPath, "credentials_path", "credentials.json", "the credentials file path")
	flag.Parse()

	configBytes, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		return fmt.Errorf("failde reading configuration file: %w", err)
	}

	if err := yaml.Unmarshal(configBytes, cfg); err != nil {
		return fmt.Errorf("failed unmarshalling configuration file: %w", err)
	}

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = "go-librespot"
	}
	if cfg.DeviceType == "" {
		cfg.DeviceType = "computer"
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = 160
	}
	if cfg.VolumeSteps == 0 {
		cfg.VolumeSteps = 100
	}

	return nil
}

func main() {
	var cfg Config
	if err := loadConfig(&cfg); err != nil {
		log.WithError(err).Fatal("failed loading config")
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

	switch cfg.Credentials.Type {
	case "zeroconf":
		if err := app.Zeroconf(); err != nil {
			log.WithError(err).Fatal("failed running zeroconf")
		}
	case "user_pass":
		if err := app.UserPass(cfg.Credentials.UserPass.Username, cfg.Credentials.UserPass.Password); err != nil {
			log.WithError(err).Fatal("failed running with username and password")
		}
	case "spotify_token":
		if err := app.SpotifyToken(cfg.Credentials.SpotifyToken.Username, cfg.Credentials.SpotifyToken.AccessToken); err != nil {
			log.WithError(err).Fatal("failed running with username and spotify token")
		}
	default:
		log.Fatalf("unknown credentials: %s", cfg.Credentials.Type)
	}
}
