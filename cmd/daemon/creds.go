package main

type SessionCredentials interface {
}

type SessionUserPassCredentials struct {
	Username string
	Password string
}

type SessionBlobCredentials struct {
	Username string
	Blob     []byte
}
