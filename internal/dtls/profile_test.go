package dtls

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestCredentialsLiteral(t *testing.T) {
	id, key, err := Credentials([]byte("user"), []byte("password"))
	want, _ := hex.DecodeString("5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8")
	if err != nil || string(id) != "user" || !bytes.Equal(key[:], want) {
		t.Fatalf("credential derivation mismatch: %v", err)
	}
	for _, v := range []struct{ i, p []byte }{{nil, []byte("p")}, {[]byte("u"), nil}, {bytes.Repeat([]byte{'u'}, 256), []byte("p")}, {[]byte("u"), bytes.Repeat([]byte{'p'}, 33)}} {
		if _, _, err := Credentials(v.i, v.p); err == nil {
			t.Fatal("accepted invalid credential length")
		}
	}
}
func TestInspectLiteral(t *testing.T) {
	// RFC 6347 record: app data, DTLS 1.2, epoch1, seq2, length3.
	p := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 2, 0, 3, 1, 2, 3}
	if n, err := ControlBytes(p); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	p[0] = 22
	if n, err := ControlBytes(p); n != len(p) || err != nil {
		t.Fatal(n, err)
	}
	for i := 0; i < len(p); i++ {
		if _, err := ControlBytes(p[:i]); err == nil {
			t.Fatalf("accepted prefix %d", i)
		}
	}
}
func TestRejectEpochZeroApplication(t *testing.T) {
	// Independent RFC 6347 envelopes: plaintext application, epoch-zero CCS,
	// and an epoch-one application. Validate the entire coalesced datagram.
	app := []byte{23, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 2, 0, 1, 42}
	control := []byte{20, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1}
	encrypted := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 3, 0, 1, 99}
	for _, records := range [][][]byte{{app}, {control, app}, {app, control}, {encrypted, app}} {
		packet := bytes.Join(records, nil)
		if _, err := ControlBytes(packet); err == nil {
			t.Fatalf("accepted epoch-zero application: %x", packet)
		}
	}
	packet := bytes.Join([][]byte{control, encrypted}, nil)
	if n, err := ControlBytes(packet); err != nil || n != len(control) {
		t.Fatalf("valid coalesced records: control bytes = %d, error = %v", n, err)
	}
}

func FuzzControlBytes(f *testing.F) {
	f.Add([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42})
	f.Add([]byte{23, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 2, 0, 1, 42})
	f.Add([]byte{20, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1,
		23, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 2, 0, 1, 42})
	f.Fuzz(func(t *testing.T, p []byte) {
		n, err := ControlBytes(p)
		if err == nil && (n < 0 || n > len(p)) {
			t.Fatal(n)
		}
	})
}
