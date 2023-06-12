package ap

type shannonConn struct {
	*dataConn

	sendKey   []byte
	sendNonce int

	recvKey   []byte
	recvNonce int
}

func newShannonConn(conn *dataConn, sendKey []byte, recvKey []byte) *shannonConn {
	// TODO
	return &shannonConn{conn, sendKey, 0, recvKey, 0}
}
