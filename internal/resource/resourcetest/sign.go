// Package resourcetest mints signed tokens for the resource server's tests.
package resourcetest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

// NewKey is a fresh RSA key to sign with.
func NewKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// Sign is a JWT access token carrying claims, signed RS256 with key.
func Sign(t testing.TB, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	return SignWith(t, jose.SigningKey{Algorithm: jose.RS256, Key: key}, claims)
}

// SignWith is a JWT carrying claims, signed with any key and algorithm.
func SignWith(t testing.TB, key jose.SigningKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(key, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return token
}
