//go:build test_unit

package zeroconf

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dh"
)

func newTestZeroconf(t *testing.T) *Zeroconf {
	z := &Zeroconf{
		log:      &librespot.NullLogger{},
		reqsChan: make(chan NewUserRequest),
	}

	var err error
	z.dh, err = dh.NewDiffieHellman()
	if err != nil {
		t.Fatalf("failed initializing diffiehellman: %v", err)
	}

	return z
}

func newAddUserRequest(t *testing.T, z *Zeroconf, username, deviceName string) *http.Request {
	clientDh, err := dh.NewDiffieHellman()
	if err != nil {
		t.Fatalf("failed initializing client diffiehellman: %v", err)
	}

	sharedSecret := clientDh.Exchange(z.dh.PublicKeyBytes())
	baseKey := func() []byte { sum := sha1.Sum(sharedSecret); return sum[:16] }()

	mac := hmac.New(sha1.New, baseKey)
	mac.Write([]byte("checksum"))
	checksumKey := mac.Sum(nil)

	mac.Reset()
	mac.Write([]byte("encryption"))
	encryptionKey := func() []byte { sum := mac.Sum(nil); return sum[:16] }()

	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("failed reading random iv: %v", err)
	}

	bc, err := aes.NewCipher(encryptionKey)
	if err != nil {
		t.Fatalf("failed initializing aes cipher: %v", err)
	}

	payload := []byte("auth-blob-payload")
	encrypted := make([]byte, len(payload))
	cipher.NewCTR(bc, iv).XORKeyStream(encrypted, payload)

	mac = hmac.New(sha1.New, checksumKey)
	mac.Write(encrypted)
	checksum := mac.Sum(nil)

	blob := append(append(append([]byte{}, iv...), encrypted...), checksum...)

	req := httptest.NewRequest(http.MethodPost, "/?action=addUser", nil)
	req.Form = url.Values{
		"userName":   {username},
		"blob":       {base64.StdEncoding.EncodeToString(blob)},
		"clientKey":  {base64.StdEncoding.EncodeToString(clientDh.PublicKeyBytes())},
		"deviceName": {deviceName},
	}
	return req
}

func TestHandleAddUserSameUserReauthenticates(t *testing.T) {
	z := newTestZeroconf(t)
	z.currentUser = "alice"

	dispatched := make(chan NewUserRequest, 1)
	go func() {
		req := <-z.reqsChan
		dispatched <- req
		req.result <- true
	}()

	rec := httptest.NewRecorder()
	if err := z.handleAddUser(rec, newAddUserRequest(t, z, "alice", "phone")); err != nil {
		t.Fatalf("handleAddUser failed: %v", err)
	}

	select {
	case req := <-dispatched:
		if req.Username != "alice" {
			t.Fatalf("dispatched request for wrong username: %s", req.Username)
		}
	default:
		t.Fatal("add user request for the current user was not dispatched")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}

	var resp AddUserResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	if resp.Status != 101 {
		t.Fatalf("unexpected response status: %d", resp.Status)
	}

	if z.currentUser != "alice" {
		t.Fatalf("unexpected current user: %s", z.currentUser)
	}
}

func TestHandleAddUserAuthenticatingSameUserShortCircuits(t *testing.T) {
	z := newTestZeroconf(t)
	z.authenticatingUser = "alice"

	rec := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() { done <- z.handleAddUser(rec, newAddUserRequest(t, z, "alice", "phone")) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleAddUser failed: %v", err)
		}
	case req := <-z.reqsChan:
		t.Fatalf("unexpected dispatch for user %s", req.Username)
	case <-time.After(time.Second):
		t.Fatal("handleAddUser did not return")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}

	var resp AddUserResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	if resp.Status != 101 {
		t.Fatalf("unexpected response status: %d", resp.Status)
	}
}
