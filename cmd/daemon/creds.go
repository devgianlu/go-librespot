package main

type SessionCredentials interface {
}

type SessionUserPassCredentials struct {
	Username string
	Password string
}

type SessionStoredCredentials struct {
	Username string
	Data     []byte
}

type SessionBlobCredentials struct {
	Username string
	Blob     []byte
}
