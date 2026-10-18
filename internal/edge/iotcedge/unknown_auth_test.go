package iotcedge

import (
	"bytes"
	"encoding/hex"
	"github.com/go-json-experiment/json"
	"os"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// Static synthetic ciphertext was sealed independently with Python's maintained
// cryptography AESGCM: key=11*16, IV=33*12. No production encoder is an oracle.
func TestUnknownLiteralAuthentication(t *testing.T) {
	raw, err := os.ReadFile("testdata/unknown-gcm.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Name, AAD, Plain, Cipher string }
	if err := json.Unmarshal(raw, &rows, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, row := range rows {
		for _, bad := range []string{"", "tag", "aad", "key", "iv"} {
			t.Run(row.Name+"/"+bad, func(t *testing.T) {
				var c IOTCCrypto
				defer c.Clear()
				key := bytes.Repeat([]byte{0x11}, 16)
				iv := [12]byte{0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33}
				if bad == "key" {
					key[0] ^= 1
				}
				if bad == "iv" {
					iv[0] ^= 1
				}
				ref, err := c.install(key, iv)
				if err != nil {
					t.Fatal(err)
				}
				a := iotc.Action{Kind: iotc.Open, Attempt: 2, Token: 20, Mode: iotc.AESGCM, Key: ref, AAD: decode(row.AAD), Data: decode(row.Cipher)}
				if bad == "tag" {
					a.Data[len(a.Data)-1] ^= 1
				}
				if bad == "aad" {
					a.AAD[5] ^= 1
				}
				e, err := c.Execute(a)
				if err != nil {
					t.Fatal(err)
				}
				if bad == "" {
					if e.Kind != iotc.Decrypted || !bytes.Equal(e.Data, decode(row.Plain)) || e.Attempt != 2 || e.Token != 20 {
						t.Fatal("literal failed authentication/context")
					}
				} else if e.Kind != iotc.CryptoRejected || len(e.Data) != 0 {
					t.Fatal("unsafe literal exposed plaintext")
				}
			})
		}
	}
}
