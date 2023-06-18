package ap

import (
	"bytes"
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/dh"
	pb "go-librespot/proto/spotify"
	"golang.org/x/crypto/pbkdf2"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type Accesspoint struct {
	addr librespot.GetAddressFunc

	nonce    []byte
	deviceId string

	dh *dh.DiffieHellman

	conn    net.Conn
	encConn *shannonConn

	stop          bool
	recvLoopStop  chan struct{}
	recvChans     map[PacketType][]chan Packet
	recvChansLock sync.RWMutex

	// reconnectLock is held for writing when performing reconnection and for reading mainly when accessing welcome
	// or sending packets. If it's not held, a valid connection (and APWelcome) is available. Be careful not to deadlock
	// anything with this.
	reconnectLock sync.RWMutex
	welcome       *pb.APWelcome
}

func NewAccesspoint(addr librespot.GetAddressFunc, deviceId string) (ap *Accesspoint, err error) {
	ap = &Accesspoint{addr: addr, deviceId: deviceId}
	ap.recvLoopStop = make(chan struct{}, 1)
	ap.recvChans = make(map[PacketType][]chan Packet)

	if err = ap.init(); err != nil {
		return nil, err
	}

	return ap, nil
}

func (ap *Accesspoint) init() (err error) {
	// read 16 nonce bytes
	ap.nonce = make([]byte, 16)
	if _, err = rand.Read(ap.nonce); err != nil {
		return fmt.Errorf("failed reading random nonce: %w", err)
	}

	// init diffiehellman parameters
	if ap.dh, err = dh.NewDiffieHellman(); err != nil {
		return fmt.Errorf("failed initializing diffiehellman: %w", err)
	}

	// open connection to accesspoint
	ap.conn, err = net.Dial("tcp", ap.addr())
	if err != nil {
		return fmt.Errorf("failed dialing accesspoint: %w", err)
	}

	return nil
}

func (ap *Accesspoint) ConnectUserPass(username, password string) error {
	return ap.Connect(&pb.LoginCredentials{
		Typ:      pb.AuthenticationType_AUTHENTICATION_USER_PASS.Enum(),
		Username: proto.String(username),
		AuthData: []byte(password),
	})
}

func (ap *Accesspoint) ConnectBlob(username string, encryptedBlob64 []byte) error {
	encryptedBlob := make([]byte, base64.StdEncoding.DecodedLen(len(encryptedBlob64)))
	if written, err := base64.StdEncoding.Decode(encryptedBlob, encryptedBlob64); err != nil {
		return fmt.Errorf("failed decodeing encrypted blob: %w", err)
	} else {
		encryptedBlob = encryptedBlob[:written]
	}

	secret := sha1.Sum([]byte(ap.deviceId))
	baseKey := pbkdf2.Key(secret[:], []byte(username), 256, 20, sha1.New)

	key := make([]byte, 24)
	copy(key, func() []byte { sum := sha1.Sum(baseKey); return sum[:] }())
	binary.BigEndian.PutUint32(key[20:], 20)

	bc, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed initializing aes cihper: %w", err)
	}

	decryptedBlob := make([]byte, len(encryptedBlob))
	for i := 0; i < len(encryptedBlob)-1; i += aes.BlockSize {
		bc.Decrypt(decryptedBlob[i:], encryptedBlob[i:])
	}

	for i := 0; i < len(decryptedBlob)-16; i++ {
		decryptedBlob[len(decryptedBlob)-i-1] ^= decryptedBlob[len(decryptedBlob)-i-17]
	}

	blob := bytes.NewReader(decryptedBlob)

	// discard first byte
	_, _ = blob.Seek(1, io.SeekCurrent)

	// discard some more bytes
	discardLen, _ := binary.ReadUvarint(blob)
	_, _ = blob.Seek(int64(discardLen), io.SeekCurrent)

	// discard another byte
	_, _ = blob.Seek(1, io.SeekCurrent)

	// read authentication type
	authTyp, _ := binary.ReadUvarint(blob)

	// discard another byte
	_, _ = blob.Seek(1, io.SeekCurrent)

	// read auth data
	authDataLen, _ := binary.ReadUvarint(blob)
	authData := make([]byte, authDataLen)
	_, _ = blob.Read(authData)

	return ap.Connect(&pb.LoginCredentials{
		Typ:      pb.AuthenticationType(authTyp).Enum(),
		Username: proto.String(username),
		AuthData: authData,
	})
}

func (ap *Accesspoint) Connect(creds *pb.LoginCredentials) error {
	// perform key exchange with diffiehellman
	exchangeData, err := ap.performKeyExchange()
	if err != nil {
		return fmt.Errorf("failed performing keyexchange: %w", err)
	}

	// solve challenge and complete connection
	if err := ap.solveChallenge(exchangeData); err != nil {
		return fmt.Errorf("failed solving challenge: %w", err)
	}

	// do authentication with credentials
	if err := ap.authenticate(creds); err != nil {
		return fmt.Errorf("failed authenticating: %w", err)
	}

	return nil
}

func (ap *Accesspoint) Close() {
	ap.stop = true
	ap.recvLoopStop <- struct{}{}
	_ = ap.conn.Close()
}

func (ap *Accesspoint) Receive(types ...PacketType) <-chan Packet {
	ch := make(chan Packet)
	ap.recvChansLock.Lock()
	for _, type_ := range types {
		ll, _ := ap.recvChans[type_]
		ll = append(ll, ch)
		ap.recvChans[type_] = ll
	}
	ap.recvChansLock.Unlock()
	return ch
}

func (ap *Accesspoint) recvLoop() {
loop:
	for {
		select {
		case <-ap.recvLoopStop:
			break loop
		default:
			// no need to hold the reconnectLock since reconnection happens in this routine
			pkt, payload, err := ap.encConn.receivePacket()
			if err != nil {
				log.WithError(err).Errorf("failed receiving packet")
				break loop
			}

			switch pkt {
			case PacketTypePing:
				if err := ap.encConn.sendPacket(PacketTypePong, payload); err != nil {
					log.WithError(err).Errorf("failed sending Pong packet")
					break loop
				}
			case PacketTypePongAck:
				continue
			default:
				ap.recvChansLock.RLock()
				ll, _ := ap.recvChans[pkt]
				ap.recvChansLock.RUnlock()

				handled := false
				for _, ch := range ll {
					ch <- Packet{Type: pkt, Payload: payload}
					handled = true
				}

				if !handled {
					log.Debugf("skipping packet %v, len: %d", pkt, len(payload))
				}
			}
		}
	}

	_ = ap.conn.Close()

	// if we shouldn't stop, try to reconnect
	if !ap.stop {
		ap.reconnectLock.Lock()
		if err := backoff.Retry(ap.reconnect, backoff.NewExponentialBackOff()); err != nil {
			log.WithError(err).Errorf("failed reconnecting accesspoint, bye bye")
			log.Exit(1)
		}
		ap.reconnectLock.Unlock()

		// reconnection was successful, do not close receivers
		return
	}

	ap.recvChansLock.RLock()
	defer ap.recvChansLock.RUnlock()
	for _, ll := range ap.recvChans {
		for _, ch := range ll {
			close(ch)
		}
	}
}

func (ap *Accesspoint) reconnect() (err error) {
	if ap.welcome == nil {
		return backoff.Permanent(fmt.Errorf("cannot reconnect without APWelcome"))
	}

	if err = ap.init(); err != nil {
		return err
	} else if err = ap.Connect(&pb.LoginCredentials{
		Typ:      ap.welcome.ReusableAuthCredentialsType,
		Username: ap.welcome.CanonicalUsername,
		AuthData: ap.welcome.ReusableAuthCredentials,
	}); err != nil {
		return err
	}

	log.Debugf("re-established accesspoint connection")
	return nil
}

func (ap *Accesspoint) performKeyExchange() ([]byte, error) {
	// accumulate transferred data for challenge
	cc := &connAccumulator{Conn: ap.conn}

	var productFlags []pb.ProductFlags
	if librespot.VersionNumberString() == "dev" {
		productFlags = []pb.ProductFlags{pb.ProductFlags_PRODUCT_FLAG_DEV_BUILD}
	} else {
		productFlags = []pb.ProductFlags{pb.ProductFlags_PRODUCT_FLAG_NONE}
	}

	// send ClientHello message
	if err := writeMessage(cc, true, &pb.ClientHello{
		BuildInfo: &pb.BuildInfo{
			Product:      pb.Product_PRODUCT_CLIENT.Enum(),
			ProductFlags: productFlags,
			Platform:     librespot.GetPlatform().Enum(),
			Version:      proto.Uint64(117300517),
		},
		CryptosuitesSupported: []pb.Cryptosuite{pb.Cryptosuite_CRYPTO_SUITE_SHANNON},
		ClientNonce:           ap.nonce,
		Padding:               []byte{0x1e},
		LoginCryptoHello: &pb.LoginCryptoHelloUnion{
			DiffieHellman: &pb.LoginCryptoDiffieHellmanHello{
				Gc:              ap.dh.PublicKeyBytes(),
				ServerKeysKnown: proto.Uint32(1),
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("failed writing ClientHello message: %w", err)
	}

	// receive APResponseMessage message
	var apResponse pb.APResponseMessage
	if err := readMessage(cc, &apResponse); err != nil {
		return nil, fmt.Errorf("failed reading APResponseMessage message: %w", err)
	}

	// verify signature
	if !verifySignature(apResponse.Challenge.LoginCryptoChallenge.DiffieHellman.Gs, apResponse.Challenge.LoginCryptoChallenge.DiffieHellman.GsSignature) {
		return nil, fmt.Errorf("failed verifying signature")
	}

	// exchange keys and compute shared secret
	ap.dh.Exchange(apResponse.Challenge.LoginCryptoChallenge.DiffieHellman.Gs)

	log.Debugf("completed keyexchange")
	return cc.Dump(), nil
}

func (ap *Accesspoint) solveChallenge(exchangeData []byte) error {
	macData := make([]byte, 0, sha1.Size*5)

	mac := hmac.New(sha1.New, ap.dh.SharedSecretBytes())
	for i := byte(1); i < 6; i++ {
		mac.Reset()
		mac.Write(exchangeData)
		mac.Write([]byte{i})
		macData = mac.Sum(macData)
	}

	mac = hmac.New(sha1.New, macData[:20])
	mac.Write(exchangeData)

	if err := writeMessage(ap.conn, false, &pb.ClientResponsePlaintext{
		PowResponse:    &pb.PoWResponseUnion{},
		CryptoResponse: &pb.CryptoResponseUnion{},
		LoginCryptoResponse: &pb.LoginCryptoResponseUnion{
			DiffieHellman: &pb.LoginCryptoDiffieHellmanResponse{
				Hmac: mac.Sum(nil),
			},
		},
	}); err != nil {
		return fmt.Errorf("failed writing ClientResponsePlaintext message: %w", err)
	}

	// set read timeout for detecting APLoginFailed message
	_ = ap.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	defer func() { _ = ap.conn.SetReadDeadline(time.Time{}) }()

	var resp pb.APResponseMessage
	if err := readMessage(ap.conn, &resp); errors.Is(err, os.ErrDeadlineExceeded) {
		ap.encConn = newShannonConn(ap.conn, macData[20:52], macData[52:84])
		log.Debug("completed challenge")
		return nil
	} else if err != nil {
		return fmt.Errorf("failed reading APLoginFailed message: %w", err)
	}

	return fmt.Errorf("failed login: %s", resp.LoginFailed.ErrorCode.String())
}

func (ap *Accesspoint) authenticate(credentials *pb.LoginCredentials) error {
	if ap.encConn == nil {
		panic("accesspoint not connected")
	}

	// assemble ClientResponseEncrypted message
	payload, err := proto.Marshal(&pb.ClientResponseEncrypted{
		LoginCredentials: credentials,
		VersionString:    proto.String(librespot.VersionString()),
		SystemInfo: &pb.SystemInfo{
			Os:                      librespot.GetOS().Enum(),
			CpuFamily:               librespot.GetCpuFamily().Enum(),
			SystemInformationString: proto.String(librespot.SystemInfoString()),
			DeviceId:                proto.String(ap.deviceId),
		},
	})
	if err != nil {
		return fmt.Errorf("failed marshalling ClientResponseEncrypted message: %w", err)
	}

	// send Login packet
	if err := ap.encConn.sendPacket(PacketTypeLogin, payload); err != nil {
		return fmt.Errorf("failed sending Login packet: %w", err)
	}

	// receive APWelcome or AuthFailure
	recvPkt, recvPayload, err := ap.encConn.receivePacket()
	if err != nil {
		return fmt.Errorf("failed recevining Login response packet: %w", err)
	}

	if recvPkt == PacketTypeAPWelcome {
		var welcome pb.APWelcome
		if err := proto.Unmarshal(recvPayload, &welcome); err != nil {
			return fmt.Errorf("failed unmarshalling APWelcome message: %w", err)
		}

		ap.welcome = &welcome
		log.Debugf("authenticated as %s", *welcome.CanonicalUsername)

		// start the recv loop
		go ap.recvLoop()

		return nil
	} else if recvPkt == PacketTypeAuthFailure {
		var loginFailed pb.APLoginFailed
		if err := proto.Unmarshal(recvPayload, &loginFailed); err != nil {
			return fmt.Errorf("failed unmarshalling APLoginFailed message: %w", err)
		}

		return fmt.Errorf("failed login: %s", loginFailed.ErrorCode.String())
	} else {
		return fmt.Errorf("unexpected command after Login packet: %x", recvPkt)
	}
}

func (ap *Accesspoint) Username() string {
	ap.reconnectLock.RLock()
	defer ap.reconnectLock.RUnlock()

	if ap.welcome == nil {
		panic("accesspoint not authenticated")
	}

	return *ap.welcome.CanonicalUsername
}

func (ap *Accesspoint) StoredCredentials() []byte {
	ap.reconnectLock.RLock()
	defer ap.reconnectLock.RUnlock()

	if ap.welcome == nil {
		panic("accesspoint not authenticated")
	}

	return ap.welcome.ReusableAuthCredentials
}
