package iotcedge

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"math/big"
	"testing"
)

func TestLicenseSignatureAndTamper(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"realm":"test"}`)
	d := sha256.Sum256(plain)
	d[0] = 0x11
	block := make([]byte, 128)
	copy(block, d[:])
	sig := new(big.Int).Exp(new(big.Int).SetBytes(block), k.D, k.N).FillBytes(make([]byte, 128))
	b := make([]byte, 132+len(plain))
	binary.LittleEndian.PutUint32(b, 1)
	copy(b[4:], sig)
	for i, v := range plain {
		b[132+i] = v ^ d[i%32]
	}
	l, err := verifyLicense(base64.StdEncoding.EncodeToString(b), &k.PublicKey)
	if err != nil || l.Realm != "test" {
		t.Fatalf("license %v", err)
	}
	// Keep the decoded JSON valid so only signature verification can reject it.
	pos := 132 + bytes.Index(plain, []byte("test"))
	b[pos] ^= 1
	if _, err = verifyLicense(base64.StdEncoding.EncodeToString(b), &k.PublicKey); err == nil {
		t.Fatal("accepted valid JSON with wrong digest")
	}
	b[pos] ^= 1
	b[len(b)-1] ^= 1
	if _, err = verifyLicense(base64.StdEncoding.EncodeToString(b), &k.PublicKey); err == nil {
		t.Fatal("accepted tamper")
	}
}
func TestIOTCCryptoAuthentication(t *testing.T) {
	var c IOTCCrypto
	m, err := c.Execute(iotc.Action{Kind: iotc.Generate, Attempt: 1, Token: 2})
	if err != nil {
		t.Fatal(err)
	}
	if m.Material.MasterNonce == m.Material.P2PNonce || m.Material.HelloSerial == 0 {
		t.Fatal("bad generated material")
	}
	a := iotc.Action{Kind: iotc.Seal, Key: m.Material.MasterRef, Mode: iotc.AESGCM, Data: []byte("payload"), AAD: []byte("context")}
	sealed, err := c.Execute(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Kind = iotc.Open
	a.Data = sealed.Data
	opened, err := c.Execute(a)
	if err != nil || opened.Kind != iotc.Decrypted || string(opened.Data) != "payload" {
		t.Fatal("decrypt failed")
	}
	a.AAD = []byte("wrong")
	opened, err = c.Execute(a)
	if err != nil || opened.Kind != iotc.CryptoRejected || len(opened.Data) != 0 {
		t.Fatal("unauthenticated plaintext exposed")
	}
	c.Clear()
	if _, err = c.Execute(a); err == nil {
		t.Fatal("disposed key reused")
	}
}

// Independent tiny RSA arithmetic fixture: 65^17 mod 3233 = 2790.
func TestRawPublicLiteral(t *testing.T) {
	b, err := rawPublic(&rsa.PublicKey{N: big.NewInt(3233), E: 17}, []byte{0, 65})
	if err != nil || len(b) != 2 || b[0] != 0x0a || b[1] != 0xe6 {
		t.Fatalf("got %x %v", b, err)
	}
}

func FuzzIOTCLicenseVerification(f *testing.F) {
	f.Add("")
	f.Add("AAAAAA==")
	f.Add(base64.StdEncoding.EncodeToString(make([]byte, 133)))
	f.Fuzz(func(t *testing.T, s string) { _, _ = VerifyIOTCLicense(s) })
}
