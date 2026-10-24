package landiscovery

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Literal fixtures encode request and reply forms independently from the encoder
// under test.
const scan = "{\"id\":7,\"method\":\"scan_iscope\",\"params\":\"\"}\r\n"

func TestDecodeRequest(t *testing.T) {
	for _, s := range []string{scan, `{"method":"scan_iscope","id":-2,"future":{},"params":null}`} {
		if _, err := decodeRequest([]byte(s)); err != nil {
			t.Fatalf("valid scan: %v", err)
		}
	}
	for _, s := range []string{`{`, `null`, `[]`, `{"id":1,"method":"reboot"}`, scan + `{}`, strings.Repeat(" ", MaxDatagram+1)} {
		if _, err := decodeRequest([]byte(s)); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	if _, err := decodeRequest(append([]byte(scan), bytes.Repeat([]byte(" "), MaxDatagram-len(scan))...)); err != nil {
		t.Fatal("4096-byte boundary rejected", err)
	}
}
func TestMinimalSynthesisAndBounds(t *testing.T) {
	m, fields, err := New("SN", "", "S50")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields, []string{"ssid", "product_model", "is_verified"}) {
		t.Fatal(fields)
	}
	var got struct {
		Result struct {
			SN, SSID string
			Verified bool `json:"is_verified"`
		}
	}
	if err := json.Unmarshal(m.Step([]byte(scan), 0), &got, json.MatchCaseInsensitiveNames(true)); err != nil || got.Result.SN != "SN" || got.Result.SSID != "S50" || !got.Result.Verified {
		t.Fatalf("minimal reply %+v, %v", got, err)
	}
	if _, _, err := New("", "name", "S50"); err == nil {
		t.Fatal("empty serial")
	}
	// Replies must fit even with the largest accepted request ID and timestamp.
	_, _, err = New("SN", strings.Repeat("<", 1000), "S50")
	if err == nil {
		t.Fatal("accepted reply that expands past app buffer")
	}
}
func TestRateLimit(t *testing.T) {
	m, _, _ := New("SN", "name", "S50")
	for i := 0; i < ReplyBurst; i++ {
		if len(m.Step([]byte(scan), 0)) == 0 {
			t.Fatalf("burst reply %d missing", i)
		}
	}
	for i := 0; i < 100; i++ {
		if m.Step([]byte(scan), time.Second-1) != nil {
			t.Fatal("rate limit bypassed")
		}
	}
	if m.Step([]byte(`{`), time.Second) != nil {
		t.Fatal("malformed replied")
	}
	if m.Step([]byte(scan), time.Second) == nil {
		t.Fatal("refill failed or malformed spent budget")
	}
	if m.Step([]byte(scan), 0) != nil {
		t.Fatal("regressing clock refilled")
	}
	if m.Step([]byte(scan), 2*time.Second) == nil {
		t.Fatal("next refill failed")
	}
}
func FuzzDecodeRequest(f *testing.F) {
	for _, s := range []string{scan, `{"method":"scan_iscope"}`, `{"id":null,"method":"scan_iscope"}`, `{"id":7,"method":42}`, `null`, `{"method":"scan_iscope","id":32766}`, `{"method":"scan_iscope","id":1,"extra":[null]}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := decodeRequest(b)
		if err == nil && (len(r.ID) == 0 || r.Code != 0 && r.Code != 100 && r.Code != 200 || r.Code == 0 && r.Method != "scan_iscope" || len(b) > MaxDatagram) {
			t.Fatal("invalid accepted request")
		}
		m, _, _ := New("SYNTHETIC", "test", "S50")
		if reply := m.Step(b, 0); len(reply) > MaxDatagram || len(reply) > 0 && !jsontext.Value(reply).IsValid() {
			t.Fatal("invalid or oversized reply")
		}
	})
}
