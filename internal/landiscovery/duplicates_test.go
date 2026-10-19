package landiscovery

import "testing"

func TestDiscoveryDuplicateNames(t *testing.T) {
	for _, frame := range []string{
		`{"method":"scan_iscope","method":"scan_iscope","id":1}`,
		`{"method":"scan_iscope","id":1,"id":2}`,
		`{"method":"scan_iscope","metho\u0064":"scan_iscope"}`,
		`{"method":"scan_iscope","future":{"a":1,"a":2}}`,
	} {
		if _, err := decodeRequest([]byte(frame)); err == nil {
			t.Fatalf("accepted duplicate: %s", frame)
		}
	}
	r, err := decodeRequest([]byte(`{"method":"scan_iscope","METHOD":"reboot","id":1,"ID":2}`))
	if err != nil || r.Method != "scan_iscope" || string(r.ID) != "1" {
		t.Fatalf("case matching: %+v, %v", r, err)
	}
}
