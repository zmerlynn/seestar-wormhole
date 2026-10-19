package edge

import (
	"bytes"
	"crypto/sha256"
	"sync"
	"testing"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
)

func TestECDHEPSKSuiteContract(t *testing.T) {
	a, b := NewECDHEPSKSuite(), NewECDHEPSKSuite()
	if a == b || a.IsInitialized() || b.IsInitialized() {
		t.Fatal("factory must return independent, uninitialized suites")
	}
	if got := a.ID(); got != 0xccac {
		t.Errorf("ID = %#x, want 0xccac", got)
	}
	if got := a.String(); got != "TLS_ECDHE_PSK_WITH_CHACHA20_POLY1305_SHA256" {
		t.Errorf("String = %q, want TLS_ECDHE_PSK_WITH_CHACHA20_POLY1305_SHA256", got)
	}
	if got := a.AuthenticationType(); got != dtls.CipherSuiteAuthenticationTypePreSharedKey {
		t.Errorf("AuthenticationType = %v, want pre-shared key", got)
	}
	if got, want := a.KeyExchangeAlgorithm(), dtls.CipherSuiteKeyExchangeAlgorithmEcdhe|dtls.CipherSuiteKeyExchangeAlgorithmPsk; got != want {
		t.Errorf("KeyExchangeAlgorithm = %v, want %v", got, want)
	}
	if !a.ECC() {
		t.Error("ECC = false, want true")
	}
	if got := a.CertificateType(); got != 0 {
		t.Errorf("CertificateType = %v, want 0", got)
	}
	hash := a.HashFunc()()
	hash.Write([]byte("suite PRF"))
	wantHash := sha256.Sum256([]byte("suite PRF"))
	if got := hash.Sum(nil); !bytes.Equal(got, wantHash[:]) {
		t.Errorf("HashFunc digest = %x, want SHA-256 %x", got, wantHash)
	}
	if err := a.Init(bytes.Repeat([]byte{1}, 48), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32), true); err != nil {
		t.Fatal(err)
	}
	if !a.IsInitialized() || b.IsInitialized() {
		t.Fatal("Init must initialize only its own factory instance")
	}
}

func TestECDHEPSKSuiteUninitializedRecords(t *testing.T) {
	peer := NewECDHEPSKSuite()
	if err := peer.Init(bytes.Repeat([]byte{1}, 48), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32), false); err != nil {
		t.Fatal(err)
	}
	raw := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 42, 0, 5, 'p', 'r', 'o', 'b', 'e'}
	pkt := &recordlayer.RecordLayer{}
	if err := pkt.Header.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	wire, err := peer.Encrypt(pkt, bytes.Clone(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		encrypt bool
		valid   bool
	}{
		{"Encrypt/empty", true, false},
		{"Encrypt/record", true, true},
		{"Decrypt/empty", false, false},
		{"Decrypt/record", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("uninitialized operation panicked: %v; want nil output and an error", p)
				}
			}()
			suite := NewECDHEPSKSuite()
			var got []byte
			var err error
			if tc.encrypt {
				if tc.valid {
					got, err = suite.Encrypt(pkt, bytes.Clone(raw))
				} else {
					got, err = suite.Encrypt(nil, nil)
				}
			} else if tc.valid {
				got, err = suite.Decrypt(recordlayer.Header{}, bytes.Clone(wire))
			} else {
				got, err = suite.Decrypt(recordlayer.Header{}, nil)
			}
			if got != nil || err != errECDHEPSKUninitialized {
				t.Fatalf("got %x, %v; want nil, %v", got, err, errECDHEPSKUninitialized)
			}
		})
	}
}

func TestECDHEPSKSuiteReinit(t *testing.T) {
	secrets := [][]byte{bytes.Repeat([]byte{0x11}, 48), bytes.Repeat([]byte{0x44}, 48)}
	client, server := bytes.Repeat([]byte{0x22}, 32), bytes.Repeat([]byte{0x33}, 32)
	raw := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 42, 0, 5, 'p', 'r', 'o', 'b', 'e'}
	pkt := &recordlayer.RecordLayer{}
	if err := pkt.Header.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	for _, role := range []struct {
		name   string
		client bool
	}{{"client", true}, {"server", false}} {
		t.Run(role.name, func(t *testing.T) {
			suite := NewECDHEPSKSuite()
			var peers [2]dtls.CipherSuite
			for i, secret := range secrets {
				if err := suite.Init(secret, client, server, role.client); err != nil {
					t.Fatal(err)
				}
				peers[i] = NewECDHEPSKSuite()
				if err := peers[i].Init(secret, client, server, !role.client); err != nil {
					t.Fatal(err)
				}
			}
			wire, err := suite.Encrypt(pkt, bytes.Clone(raw))
			if err != nil {
				t.Fatal(err)
			}
			for i, peer := range peers {
				got, err := peer.Decrypt(recordlayer.Header{}, bytes.Clone(wire))
				if i == 0 {
					if err == nil || got != nil {
						t.Fatalf("old keys decrypted re-Init record: %x, %v; want nil and an error", got, err)
					}
				} else if err != nil || len(got) < 13 || !bytes.Equal(got[13:], raw[13:]) {
					t.Fatalf("new keys decrypted %x, %v; want payload %x", got, err, raw[13:])
				}
				incoming, err := peer.Encrypt(pkt, bytes.Clone(raw))
				if err != nil {
					t.Fatal(err)
				}
				got, err = suite.Decrypt(recordlayer.Header{}, incoming)
				if i == 0 {
					if err == nil || got != nil {
						t.Fatalf("re-Init accepted old-key record: %x, %v; want nil and an error", got, err)
					}
				} else if err != nil || len(got) < 13 || !bytes.Equal(got[13:], raw[13:]) {
					t.Fatalf("re-Init decrypted %x, %v; want new-key payload %x", got, err, raw[13:])
				}
			}
		})
	}
}

func TestECDHEPSKSuiteConcurrentInitAndRecords(t *testing.T) {
	secrets := [][]byte{bytes.Repeat([]byte{1}, 48), bytes.Repeat([]byte{4}, 48)}
	client, server := bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)
	raw := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 42, 0, 5, 'p', 'r', 'o', 'b', 'e'}
	var header recordlayer.Header
	if err := header.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	var want, incoming [2][]byte
	for i, secret := range secrets {
		reference, peer := NewECDHEPSKSuite(), NewECDHEPSKSuite()
		if err := reference.Init(secret, client, server, i == 0); err != nil {
			t.Fatal(err)
		}
		if err := peer.Init(secret, client, server, i != 0); err != nil {
			t.Fatal(err)
		}
		var err error
		want[i], err = reference.Encrypt(&recordlayer.RecordLayer{Header: header}, bytes.Clone(raw))
		if err != nil {
			t.Fatal(err)
		}
		incoming[i], err = peer.Encrypt(&recordlayer.RecordLayer{Header: header}, bytes.Clone(raw))
		if err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Equal(want[0], want[1]) {
		t.Fatal("fixtures must distinguish the two key generations")
	}
	suite := NewECDHEPSKSuite()
	if err := suite.Init(secrets[0], client, server, true); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			<-start
			for i := range 256 {
				switch worker % 4 {
				case 0:
					generation := (i + worker/4) % 2
					if err := suite.Init(secrets[generation], client, server, generation == 0); err != nil {
						t.Error(err)
						return
					}
				case 1:
					if !suite.IsInitialized() {
						t.Error("published state disappeared during re-Init")
						return
					}
				case 2:
					got, err := suite.Encrypt(&recordlayer.RecordLayer{Header: header}, bytes.Clone(raw))
					// Matching exactly one reference rejects a mixture of keys/IVs.
					if err != nil || bytes.Equal(got, want[0]) == bytes.Equal(got, want[1]) {
						t.Errorf("record %x, %v; want exactly one complete key generation", got, err)
						return
					}
				case 3:
					got, err := suite.Decrypt(recordlayer.Header{}, bytes.Clone(incoming[i%2]))
					// The active generation may reject the other generation's record.
					if err != nil {
						if got != nil {
							t.Errorf("Decrypt returned %x with error %v, want nil output", got, err)
							return
						}
					} else if len(got) < 13 || !bytes.Equal(got[13:], raw[13:]) {
						t.Errorf("Decrypt = %x, want payload %x", got, raw[13:])
						return
					}
				}
			}
		})
	}
	close(start)
	wg.Wait()
	if !suite.IsInitialized() {
		t.Fatal("concurrent Init never published state")
	}
}
