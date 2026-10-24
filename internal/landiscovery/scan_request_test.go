package landiscovery

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
)

// Synthetic iOS-form scan request.
// Only id and method are consumed; root name/ip do not require params.
func TestScanIOSRequest(t *testing.T) {
	const wire = "{\"id\":17,\"method\":\"scan_iscope\",\"name\":\"synthetic\",\"ip\":\"192.0.2.17\"}\r\n"
	m, _, err := New("SYNTHETIC", "test", "S50")
	if err != nil {
		t.Fatal(err)
	}
	if reply := m.Step([]byte(wire), 0); len(reply) == 0 {
		t.Fatal("iOS request got no synthesized reply")
	}
}

// Only id/method/params are consumed, params is optional and ignored,
// and id may be any JSON value.
func TestScanRequestDispatch(t *testing.T) {
	android := []byte("{\"id\":17,\"method\":\"scan_iscope\",\"params\":\"\"}\r\n")
	ios := []byte("{\"id\":17,\"method\":\"scan_iscope\",\"name\":\"synthetic\",\"ip\":\"192.0.2.17\"}\r\n")
	a, _, _ := New("SYNTHETIC", "test", "S50")
	b, _, _ := New("SYNTHETIC", "test", "S50")
	if x, y := a.Step(android, 0), b.Step(ios, 0); len(x) == 0 || !bytes.Equal(x, y) {
		t.Fatalf("Android/iOS differ: %s / %s", x, y)
	}
	for _, tc := range []struct {
		wire, id string
		code     int
	}{
		{`{"method":"scan_iscope"}`, `null`, 200},
		{`{"id":7}`, `7`, 100},
		{`{"id":7,"method":null}`, `7`, 100},
		{`{"id":7,"method":42}`, `7`, 100},
		{`{"id":7,"method":{}}`, `7`, 100},
		{`{"method":42}`, `null`, 200},
		{`{"id":null,"method":"scan_iscope"}`, `null`, 0},
		{`{"id":1.5,"method":"scan_iscope","params":false}`, `1.5`, 0},
		{`{"id":"test","method":"scan_iscope","params":{}}`, `"test"`, 0},
		{`{"id":[1],"method":"scan_iscope","params":[]}`, `[1]`, 0},
		{`{"id":{"opaque":true},"method":"scan_iscope","name":{},"ip":false}`, `{"opaque":true}`, 0},
	} {
		t.Run(tc.wire, func(t *testing.T) {
			m, _, _ := New("SYNTHETIC", "test", "S50")
			raw := m.Step([]byte(tc.wire+"\r\n"), 0)
			var got struct {
				ID   jsontext.Value
				Code int
			}
			if err := json.Unmarshal(raw, &got, json.MatchCaseInsensitiveNames(true)); err != nil || string(got.ID) != tc.id || got.Code != tc.code {
				t.Fatalf("reply=%s err=%v, want id=%s code=%d", raw, err, tc.id, tc.code)
			}
		})
	}
	for _, raw := range []string{`{"id":7,"method":"Scan_iscope"}`, `{"id":7,"method":"unknown"}`} {
		m, _, _ := New("SYNTHETIC", "test", "S50")
		if got := m.Step([]byte(raw), 0); got != nil {
			t.Fatalf("unknown method replied: %s", got)
		}
	}
}

func TestScanReplyBound(t *testing.T) {
	m, _, _ := New("SYNTHETIC", "test", "S50")
	raw := `{"id":"` + strings.Repeat("x", MaxDatagram-40) + `","method":"scan_iscope"}`
	if len(raw) > MaxDatagram {
		t.Fatal("fixture exceeds request bound")
	}
	if got := m.Step([]byte(raw), 0); got != nil {
		t.Fatalf("oversize id echo escaped reply bound: %d bytes", len(got))
	}
}
