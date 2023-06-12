package ap

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	pb "go-librespot/proto/base"
	"google.golang.org/protobuf/proto"
	"net"
	"os"
	"time"
)

type AccessPoint struct {
	nonce []byte

	dh *diffieHellman

	conn    net.Conn
	encConn *shannonConn
}

func NewAccessPoint(host string, port int) (ap *AccessPoint, err error) {
	ap = &AccessPoint{}

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
	ap.conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", host, port))
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

func (ap *AccessPoint) performKeyExchange() ([]byte, error) {
	// accumulate transferred data for challenge
	cc := &connAccumulator{Conn: ap.conn}

	// send ClientHello message
	if err := writeMessage(cc, true, &pb.ClientHello{
		BuildInfo: &pb.BuildInfo{
			Product:      pb.Product_PRODUCT_CLIENT.Enum(),
			ProductFlags: []pb.ProductFlags{pb.ProductFlags_PRODUCT_FLAG_NONE},
			Platform:     pb.Platform_PLATFORM_WIN32_X86.Enum(),
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
