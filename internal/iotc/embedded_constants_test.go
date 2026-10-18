package iotc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"os"
	"testing"
)

// Literal DER fingerprints of the public keys derived from the shipped binary.
// PEM armor and whitespace are excluded. Check compiled keys and SDK fixture copies.
func TestEmbeddedPublicKeys(t *testing.T) {
	for _, tc := range []struct {
		name, compiled, fixture, fingerprint string
		derBytes                             int
	}{
		{"license", LicensePublicKeyPEM, "testdata/device-license-public.pem", "75063ce243f456370dea2efa502d42f18d8c6a817c989f569d22b0bb5f6fb45d", 162},
		{"master", MasterPublicKeyPEM, "testdata/client-master-public.pem", "ec8f2dbd0933aa6dd3dc3c383c6532f720e7767ba75cd8df2ddb5b4188cda8ab", 294},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			for _, source := range []struct {
				name string
				data []byte
			}{{"compiled", []byte(tc.compiled)}, {"fixture", fixture}} {
				t.Run(source.name, func(t *testing.T) {
					block, rest := pem.Decode(source.data)
					if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
						t.Fatal("want exactly one PUBLIC KEY PEM block")
					}
					if len(block.Bytes) != tc.derBytes {
						t.Errorf("DER length = %d, want %d", len(block.Bytes), tc.derBytes)
					}
					digest := sha256.Sum256(block.Bytes)
					if got := hex.EncodeToString(digest[:]); got != tc.fingerprint {
						t.Errorf("DER SHA-256 = %s, want %s", got, tc.fingerprint)
					}
				})
			}
		})
	}
}

// Only the first 16 phrase bytes derived from the shipped binary are used.
// The full 32-byte ASCII phrase has SHA-256
// 34a672db0239cae3b78f313bf330f19f377828106681cc042535965b638115cf.
func TestEmbeddedLegacyPrefix(t *testing.T) {
	const phrase = "Charlie is the designer of P2P!!"
	key := ScrambleKey([16]byte{})
	if got, want := string(key[:]), phrase[:16]; got != want {
		t.Errorf("legacy prefix = %q, want %q", got, want)
	}
	digest := sha256.Sum256(key[:])
	const fingerprint = "84010804d0e390f2566dae273324a53bc15cade23cc6778da0b476d4556f6e54"
	if got := hex.EncodeToString(digest[:]); got != fingerprint {
		t.Errorf("prefix SHA-256 = %s, want %s", got, fingerprint)
	}
}

// Assert literal host names using suffixes derived from the shipped binary.
// The cn suffix applies only to exactly "cn", and dotted realms receive no suffix.
func TestEmbeddedMasterSuffixes(t *testing.T) {
	for _, tc := range []struct{ name, realm, region, want string }{
		{"default", "test", "all", "all-c-master-test.iotcplatform.com"},
		{"default-region", "test", "", "all-c-master-test.iotcplatform.com"},
		{"cn", "test", "cn", "cn-c-master-test.kalay.net.cn"},
		{"uppercase-region", "test", "CN", "CN-c-master-test.iotcplatform.com"},
		{"dotted-realm", "test.invalid", "cn", "cn-c-master-test.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (License{Realm: tc.realm}).MasterDomain(tc.region)
			if err != nil || got != tc.want {
				t.Errorf("MasterDomain(%q) = %q, %v; want %q, nil", tc.region, got, err, tc.want)
			}
		})
	}
}
