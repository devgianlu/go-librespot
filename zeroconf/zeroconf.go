package zeroconf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dh"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	"github.com/grandcat/zeroconf"
	log "github.com/sirupsen/logrus"
)

type Zeroconf struct {
	log *log.Entry

	deviceName string
	deviceId   string
	deviceType devicespb.DeviceType

	listener net.Listener
	server   *zeroconf.Server

	dh *dh.DiffieHellman

	reqsChan chan NewUserRequest

	authenticatingUser string
	currentUser        string
	userLock           sync.RWMutex
}

type NewUserRequest struct {
	Username   string
	AuthBlob   []byte
	DeviceName string

	result chan bool
}

func NewZeroconf(log *log.Entry, port int, deviceName, deviceId string, deviceType devicespb.DeviceType) (_ *Zeroconf, err error) {
	z := &Zeroconf{log: log, deviceId: deviceId, deviceName: deviceName, deviceType: deviceType}
	z.reqsChan = make(chan NewUserRequest)

	z.dh, err = dh.NewDiffieHellman()
	if err != nil {
		return nil, fmt.Errorf("failed initializing diffiehellman: %w", err)
	}

	z.listener, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("failed starting zeroconf listener: %w", err)
	}

	listenPort := z.listener.Addr().(*net.TCPAddr).Port
	log.Infof("zeroconf server listening on port %d", listenPort)

	z.server, err = zeroconf.Register(deviceName, "_spotify-connect._tcp", "local.", listenPort, []string{"CPath=/", "VERSION=1.0", "Stack=SP"}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed registering zeroconf server: %w", err)
	}

	return z, nil
}

func (z *Zeroconf) SetCurrentUser(username string) {
	z.userLock.Lock()
	z.currentUser = username
	z.userLock.Unlock()
}

// Close stops the zeroconf responder and HTTP listener,
// but does not close the last opened session.
func (z *Zeroconf) Close() {
	z.server.Shutdown()
	_ = z.listener.Close()
}

func (z *Zeroconf) handleGetInfo(writer http.ResponseWriter, _ *http.Request) error {
	writer.WriteHeader(http.StatusOK)

	// get current username holding the client
	z.userLock.RLock()
	currentUsername := z.currentUser
	z.userLock.RUnlock()

	return json.NewEncoder(writer).Encode(GetInfoResponse{
		Status:           101,
		StatusString:     "OK",
		SpotifyError:     0,
		Version:          "2.7.1",
		LibraryVersion:   librespot.VersionNumberString(),
		AccountReq:       "PREMIUM",
		BrandDisplayName: "devgianlu",
		ModelDisplayName: "go-librespot",
		VoiceSupport:     "NO",
		Availability:     "",
		ProductID:        0,
		TokenType:        "default",
		GroupStatus:      "NONE",
		ResolverVersion:  "0",
		Scope:            "streaming,client-authorization-universal",

		DeviceID:   z.deviceId,
		RemoteName: z.deviceName,
		PublicKey:  base64.StdEncoding.EncodeToString(z.dh.PublicKeyBytes()),
		DeviceType: z.deviceType.String(),
		ActiveUser: currentUsername,
	})
}

func (z *Zeroconf) handleAddUser(writer http.ResponseWriter, request *http.Request) error {
	username, blobStr, clientKeyStr, deviceName := request.Form.Get("userName"), request.Form.Get("blob"), request.Form.Get("clientKey"), request.Form.Get("deviceName")
	if len(username) == 0 {
		return fmt.Errorf("missing username")
	} else if len(blobStr) == 0 {
		return fmt.Errorf("missing blob")
	} else if len(clientKeyStr) == 0 {
		return fmt.Errorf("missing client key")
	} else if len(deviceName) == 0 {
		return fmt.Errorf("missing device name")
	}

	blob, err := base64.StdEncoding.DecodeString(blobStr)
	if err != nil {
		return fmt.Errorf("invalid blob: %w", err)
	}

	clientKey, err := base64.StdEncoding.DecodeString(clientKeyStr)
	if err != nil {
		return fmt.Errorf("invalid client key: %w", err)
	}

	// start handshake and decrypting of blob
	sharedSecret := z.dh.Exchange(clientKey)
	baseKey := func() []byte { sum := sha1.Sum(sharedSecret); return sum[:16] }()
	iv, encrypted, checksum := blob[:16], blob[16:len(blob)-20], blob[len(blob)-20:]

	mac := hmac.New(sha1.New, baseKey)
	mac.Write([]byte("checksum"))
	checksumKey := mac.Sum(nil)

	mac.Reset()
	mac.Write([]byte("encryption"))
	encryptionKey := func() []byte { sum := mac.Sum(nil); return sum[:16] }()

	mac = hmac.New(sha1.New, checksumKey)
	mac.Write(encrypted)
	if !bytes.Equal(mac.Sum(nil), checksum) {
		z.log.Warnf("zeroconf received request with bad checksum")
		writer.WriteHeader(http.StatusBadRequest)
		return nil
	}

	bc, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return fmt.Errorf("failed initializing aes cihper: %w", err)
	}

	decrypted := make([]byte, len(encrypted))
	cipher.NewCTR(bc, iv).XORKeyStream(decrypted, encrypted)

	z.userLock.Lock()

	// check if we are authenticating the same user that is holding the session
	if z.currentUser == username || z.authenticatingUser == username {
		z.userLock.Unlock()

		writer.WriteHeader(http.StatusOK)
		return json.NewEncoder(writer).Encode(AddUserResponse{
			Status:       101,
			StatusString: "OK",
			SpotifyError: 0,
		})
	}

	if z.authenticatingUser != "" {
		z.userLock.Unlock()

		z.log.Debug("zeroconf is authenticating another user")
		writer.WriteHeader(http.StatusForbidden)
		return nil
	}

	z.authenticatingUser = username
	z.userLock.Unlock()

	// dispatch user request and wait response
	resChan := make(chan bool)
	z.reqsChan <- NewUserRequest{
		Username:   username,
		AuthBlob:   decrypted,
		DeviceName: deviceName,
		result:     resChan,
	}

	// wait for auth result
	res := <-resChan

	z.userLock.Lock()
	if !res {
		z.authenticatingUser = ""
		z.userLock.Unlock()

		z.log.Infof("refused zeroconf user %s from %s", username, deviceName)
		writer.WriteHeader(http.StatusForbidden)
		return nil
	}

	// update user holding the session
	z.authenticatingUser = ""
	z.currentUser = username
	z.userLock.Unlock()

	z.log.Infof("accepted zeroconf user %s from %s", username, deviceName)

	writer.WriteHeader(http.StatusOK)
	return json.NewEncoder(writer).Encode(AddUserResponse{
		Status:       101,
		StatusString: "OK",
		SpotifyError: 0,
	})
}

type HandleNewRequestFunc func(req NewUserRequest) bool

func (z *Zeroconf) Serve(handler HandleNewRequestFunc) error {
	defer z.server.Shutdown()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			z.log.WithError(err).Warn("failed handling invalid request form")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}

		writer.Header().Set("Content-Type", "application/json")

		action := request.Form.Get("action")
		switch action {
		case "getInfo":
			if err := z.handleGetInfo(writer, request); err != nil {
				z.log.WithError(err).Warn("failed handling zeroconf get info request")
				writer.WriteHeader(http.StatusInternalServerError)
			}
		case "addUser":
			if err := z.handleAddUser(writer, request); err != nil {
				z.log.WithError(err).Warn("failed handling zeroconf add user request")
				writer.WriteHeader(http.StatusInternalServerError)
			}
		default:
			z.log.Warnf("unknown zeroconf action: %s", action)
			writer.WriteHeader(http.StatusBadRequest)
		}
	})

	serveErr := make(chan error, 1)
	go func() { serveErr <- http.Serve(z.listener, mux) }()

	for {
		select {
		case err := <-serveErr:
			return err
		case req := <-z.reqsChan:
			req.result <- handler(req)
		}
	}
}
