package landiscovery

import (
	"bytes"
	jsonv1 "encoding/json"
	"testing"
)

// Compare synthesized discovery bytes, including nonnil empty strings, to v1.
func TestDiscoveryJSONV1Bytes(t *testing.T) {
	for _, name := range []string{"", "<>&\u2028\u2029"} {
		m, _, err := New("SN", name, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range []string{`{"id":1,"method":"scan_iscope"}`, `{"id":"\u0061","method":null}`} {
			got := m.Step([]byte(frame), 0)
			var r reply
			if err := jsonv1.Unmarshal(got, &r); err != nil {
				t.Fatal(err)
			}
			want, err := jsonv1.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("discovery=%s; v1=%s", got, want)
			}
		}
	}
}
