package ap

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	pb "go-librespot/proto/spotify"
	"google.golang.org/protobuf/proto"
	"net"
	"os"
	"sync"
	"time"
)

type AccessPoint struct {
	nonce []byte

	dh *diffieHellman

	conn    net.Conn
	encConn *shannonConn

	recvLoopStop  chan struct{}
	recvChans     map[PacketType][]chan Packet
	recvChansLock sync.RWMutex
}

func NewAccessPoint(addr string) (ap *AccessPoint, err error) {
	ap = &AccessPoint{}
	ap.recvLoopStop = make(chan struct{}, 1)
	ap.recvChans = make(map[PacketType][]chan Packet)

	// read 16 nonce bytes
	ap.nonce = make([]byte, 16)
	if _, err = rand.Read(ap.nonce); err != nil {
		return nil, fmt.Errorf("failed reading random nonce: %w", err)
	}

	// init diffiehellman parameters
	if ap.dh, err = newDiffieHellman(); err != nil {
		return nil, fmt.Errorf("failed initializing diffiehellman: %w", err)
	}

	// open connection to accesspoint
	ap.conn, err = net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed dialing accesspoint: %w", err)
	}

	return ap, nil
}

func (ap *AccessPoint) Connect() (err error) {
	// perform key exchange with diffiehellman
	var exchangeData []byte
	exchangeData, err = ap.performKeyExchange()
	if err != nil {
		return fmt.Errorf("failed performing keyexchange: %w", err)
	}

	// solve challenge and complete connection
	if err := ap.solveChallenge(exchangeData); err != nil {
		return fmt.Errorf("failed solving challenge: %w", err)
	}

	return nil
}

func (ap *AccessPoint) Authenticate(username, password, deviceId string) error {
	if ap.encConn == nil {
		panic("accesspoint not connected")
	}

	if err := ap.authenticate(&pb.LoginCredentials{
		Typ:      pb.AuthenticationType_AUTHENTICATION_USER_PASS.Enum(),
		Username: proto.String(username),
		AuthData: []byte(password),
	}, deviceId); err != nil {
		return fmt.Errorf("failed authenticating: %w", err)
	}

	// start the recv loop
	go ap.recvLoop()

	return nil
}

func (ap *AccessPoint) Close() {
	ap.recvLoopStop <- struct{}{}
	_ = ap.conn.Close()
}

func (ap *AccessPoint) Receive(types ...PacketType) <-chan Packet {
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

func (ap *AccessPoint) recvLoop() {
loop:
	for {
		select {
		case <-ap.recvLoopStop:
			break loop
		default:
			pkt, payload, err := ap.encConn.receivePacket()
			if err != nil {
				log.WithError(err).Errorf("failed receiving packet")
				break loop
			}

			switch pkt {
			case PacketTypePing:
				if err := ap.encConn.sendPacket(PacketTypePong, payload); err != nil {
					log.WithError(err).Errorf("failed sending Pong packet")
					return
				}
			case PacketTypePongAck:
				continue
			default:
				ap.recvChansLock.RLock()
				handled := false
				ll, _ := ap.recvChans[pkt]
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

	ap.recvChansLock.RLock()
	defer ap.recvChansLock.RUnlock()
	for _, ll := range ap.recvChans {
		for _, ch := range ll {
			close(ch)
		}
	}
}

func (ap *AccessPoint) performKeyExchange() ([]byte, error) {
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
				Gc:              ap.dh.publicKeyBytes(),
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
	ap.dh.exchange(apResponse.Challenge.LoginCryptoChallenge.DiffieHellman.Gs)

	log.Debugf("completed keyexchange")
	return cc.Dump(), nil
}

func (ap *AccessPoint) solveChallenge(exchangeData []byte) error {
	macData := make([]byte, 0, sha1.Size*5)

	mac := hmac.New(sha1.New, ap.dh.sharedSecret)
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

func (ap *AccessPoint) authenticate(credentials *pb.LoginCredentials, deviceId string) error {
	// assemble ClientResponseEncrypted message
	payload, err := proto.Marshal(&pb.ClientResponseEncrypted{
		LoginCredentials: credentials,
		VersionString:    proto.String(librespot.VersionString()),
		SystemInfo: &pb.SystemInfo{
			Os:                      librespot.GetOS().Enum(),
			CpuFamily:               librespot.GetCpuFamily().Enum(),
			SystemInformationString: proto.String(librespot.SystemInfoString()),
			DeviceId:                proto.String(deviceId),
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

		log.Debugf("authenticated as %s", *welcome.CanonicalUsername)
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
