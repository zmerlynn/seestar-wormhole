package iotcedge

import (
	"bytes"
	"encoding/hex"
	"github.com/go-json-experiment/json"
	"os"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// Independently sealed static C06a/b fixtures use the same visible SID, UID and
// master nonce in both attempts. Only the attempt key changes (11*16 -> 22*16).
func TestLayoutMasterPreviousAttemptAuthentication(t *testing.T) {
	raw, err := os.ReadFile("testdata/layout-attempt-gcm.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Name, Plain, AAD, Old, Current string }
	if err = json.Unmarshal(raw, &rows, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	decode := func(s string) []byte {
		t.Helper()
		b, e := hex.DecodeString(s)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			var old, current IOTCCrypto
			iv := [12]byte{0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33}
			oldRef, err := old.install(bytes.Repeat([]byte{0x11}, 16), iv)
			if err != nil {
				t.Fatal(err)
			}
			a := iotc.Action{Kind: iotc.Open, Attempt: 1, Token: 10, Mode: iotc.AESGCM, Key: oldRef, AAD: decode(row.AAD), Data: decode(row.Old)}
			e, err := old.Execute(a)
			if err != nil || e.Kind != iotc.Decrypted || !bytes.Equal(e.Data, decode(row.Plain)) {
				t.Fatal("old fixture authentication failed")
			}
			old.Clear()
			currentRef, err := current.install(bytes.Repeat([]byte{0x22}, 16), iv)
			if err != nil {
				t.Fatal(err)
			}
			// The replacement socket supplies the current attempt/token; the old
			// ciphertext must still fail even though all public identities are equal.
			a.Attempt, a.Token, a.Key = 2, 20, currentRef
			e, err = current.Execute(a)
			if err != nil || e.Kind != iotc.CryptoRejected || len(e.Data) != 0 {
				t.Fatal("old ciphertext exposed plaintext on replacement attempt")
			}
			a.Data = decode(row.Current)
			e, err = current.Execute(a)
			if err != nil || e.Kind != iotc.Decrypted || e.Attempt != 2 || e.Token != 20 || !bytes.Equal(e.Data, decode(row.Plain)) {
				t.Fatal("current fixture failed after old ciphertext rejection")
			}
			current.Clear()
		})
	}
}
