package iotcedge

import (
	"bytes"
	"encoding/base64"
	"os"
	"testing"
)

func TestSignedLicenseElevenLiteral(t *testing.T) {
	// Static synthetic RSA envelope generated independently of the Go codec.
	pem, err := os.ReadFile("../../iotc/testdata/license-synthetic-public.pem")
	if err != nil {
		t.Fatal(err)
	}
	key, err := publicRSA(string(pem))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../iotc/testdata/license-eleven.b64")
	if err != nil {
		t.Fatal(err)
	}
	l, err := verifyLicense(string(fixture), key)
	if err != nil {
		t.Fatalf("signed eleven-entry license rejected: %v", err)
	}
	if l.Realm != "test" || len(l.Standalone) != 10 || l.Standalone["j"] != "j.invalid" || l.Standalone["k"] != "" {
		t.Fatalf("unexpected retained settings: %v", l)
	}
	// Change a retained domain without changing JSON shape or domain validity.
	wire, err := base64.StdEncoding.DecodeString(string(fixture))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile("../../iotc/testdata/license-eleven.json")
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(plain, []byte("a.invalid"))
	if index < 0 {
		t.Fatal("missing literal domain")
	}
	wire[132+index] ^= 'a' ^ 'z'
	l, err = verifyLicense(base64.StdEncoding.EncodeToString(wire), key)
	if err == nil || l.Realm != "" || len(l.Standalone) != 0 {
		t.Fatalf("unauthenticated settings exposed: %v, %v", l, err)
	}
}

// Verification carries the bounded omission count to the layer owner. It does
// not choose a diagnostic sink; unauthenticated settings are never exposed.
func TestLicenseRetentionDiagnosticOutcome(t *testing.T) {
	pem, err := os.ReadFile("../../iotc/testdata/license-synthetic-public.pem")
	if err != nil {
		t.Fatal(err)
	}
	key, err := publicRSA(string(pem))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../iotc/testdata/license-eleven.b64")
	if err != nil {
		t.Fatal(err)
	}
	l, err := verifyLicense(string(fixture), key)
	if err != nil || l.StandaloneOmitted != 1 {
		t.Fatalf("omission count=%d err=%v, want 1/nil", l.StandaloneOmitted, err)
	}
	wire, err := base64.StdEncoding.DecodeString(string(fixture))
	if err != nil {
		t.Fatal(err)
	}
	wire[len(wire)-1] ^= 1
	l, err = verifyLicense(base64.StdEncoding.EncodeToString(wire), key)
	if err == nil || l.StandaloneOmitted != 0 {
		t.Fatal("unauthenticated license exposed omission count")
	}
}
