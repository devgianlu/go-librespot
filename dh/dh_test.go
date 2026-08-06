//go:build test_unit

package dh

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

// Both sides of an exchange must land on the same secret, which is the whole
// point of the handshake the accesspoint depends on.
func TestExchangeAgreesOnBothSides(t *testing.T) {
	client, err := NewDiffieHellman()
	require.NoError(t, err)

	server, err := NewDiffieHellman()
	require.NoError(t, err)

	clientSecret := client.Exchange(server.PublicKeyBytes())
	serverSecret := server.Exchange(client.PublicKeyBytes())

	require.NotEmpty(t, clientSecret)
	require.Equal(t, serverSecret, clientSecret)
}

func TestExchangeStoresSharedSecret(t *testing.T) {
	local, err := NewDiffieHellman()
	require.NoError(t, err)

	remote, err := NewDiffieHellman()
	require.NoError(t, err)

	require.Nil(t, local.SharedSecretBytes(), "no secret before an exchange")

	secret := local.Exchange(remote.PublicKeyBytes())
	require.Equal(t, secret, local.SharedSecretBytes())
}

// Two keypairs must not collide: a fixed private key would silently make every
// session use the same secret.
func TestNewDiffieHellmanGeneratesDistinctKeys(t *testing.T) {
	first, err := NewDiffieHellman()
	require.NoError(t, err)

	second, err := NewDiffieHellman()
	require.NoError(t, err)

	require.NotEqual(t, first.PublicKeyBytes(), second.PublicKeyBytes())
	require.NotEqual(t, first.privateKey.Bytes(), second.privateKey.Bytes())
}

// The public key is g^private mod p; check it against the group parameters
// rather than trusting the implementation to agree with itself.
func TestPublicKeyMatchesGroupParameters(t *testing.T) {
	local, err := NewDiffieHellman()
	require.NoError(t, err)

	expected := new(big.Int).Exp(dhGenerator, local.privateKey, dhPrime)
	require.Equal(t, expected.Bytes(), local.PublicKeyBytes())

	// A 95 byte private key stays well inside the 96 byte prime, so the public
	// key must too.
	require.LessOrEqual(t, len(local.PublicKeyBytes()), len(dhPrime.Bytes()))
}

// The prime is the 768 bit MODP group (RFC 2409 group 1) with generator 2,
// which is what Spotify's handshake expects: the exchange will not
// interoperate if either is ever altered.
func TestGroupParametersAreTheExpectedMODPGroup(t *testing.T) {
	require.Equal(t, big.NewInt(2), dhGenerator)
	require.Equal(t, 768, dhPrime.BitLen())
	require.True(t, dhPrime.ProbablyPrime(20))
}

// Exchange is driven by bytes off the wire, so it must not panic on the
// degenerate values a peer could send.
func TestExchangeToleratesDegenerateRemoteKeys(t *testing.T) {
	for name, remote := range map[string][]byte{
		"empty": {},
		"zero":  {0x00},
		"one":   {0x01},
	} {
		t.Run(name, func(t *testing.T) {
			local, err := NewDiffieHellman()
			require.NoError(t, err)

			require.NotPanics(t, func() { local.Exchange(remote) })
		})
	}
}
