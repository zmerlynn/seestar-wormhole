package iotcedge

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

func TestGeneratedKeysReachOnlyWriter(t *testing.T) {
	path := t.TempDir() + "/keys"
	w, err := keylog.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	c := IOTCCrypto{KeyLog: w}
	defer c.Clear()
	e, err := c.Execute(iotc.Action{Kind: iotc.Generate, Attempt: 7})
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Execute(iotc.Action{Kind: iotc.Derive, Attempt: 7, Private: 1, Public: e.Material.Public})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	b, _ := os.ReadFile(path)
	want := fmt.Sprintf("# IOTC_KEY 7 %x 3 %x %x\n", e.Material.RID, e.Material.MasterKey, e.Material.MasterIV)
	if !strings.HasPrefix(string(b), want) {
		t.Fatal("master material missing")
	}
	if strings.Count(string(b), "# IOTC_KEY 7 ") != 2 {
		t.Fatal("derived key missing")
	}
	// Independently decrypt a real crypto effect using only the saved server pair.
	var attempt, ref uint64
	var rid, key, iv []byte
	line := strings.Split(strings.TrimSpace(string(b)), "\n")[1]
	if _, err := fmt.Sscanf(line, "# IOTC_KEY %d %x %d %x %x", &attempt, &rid, &ref, &key, &iv); err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Execute(iotc.Action{Kind: iotc.Seal, Mode: iotc.AESGCM, Key: d.Key, Data: []byte("fixture payload"), AAD: []byte("fixture envelope")})
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, iv, sealed.Data, []byte("fixture envelope"))
	if err != nil || string(plain) != "fixture payload" || attempt != 7 || ref != uint64(d.Key) || !bytes.Equal(rid, e.Material.RID[:]) {
		t.Fatal("saved server pair cannot decrypt", err)
	}
	// Closing diagnostics must not prevent further key derivation.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = c.Execute(iotc.Action{Kind: iotc.Derive, Attempt: 7, Private: 1, Public: e.Material.Public})
	if err != nil {
		t.Fatal("diagnostic failure reached crypto")
	}
}
