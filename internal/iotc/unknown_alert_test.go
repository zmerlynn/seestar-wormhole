package iotc

import "testing"

// Unknown alerts are logged
// only after nonce checking, without even installing an empty server SID (SHOULD).
func TestUnknownAlertNonceCheck(t *testing.T) {
	for _, sid := range []uint32{0, 0x55} {
		for _, literal := range []string{"55000000341203041d", "55000000351203041d", "550000003412ff041d"} {
			m := terminalMachine(t, 1)
			m.endpoints[0].sid = sid
			before := m.endpoints
			m.out = nil
			m.decrypted(operation{typ: 14, link: m.endpoints[0].link}, unhex(literal))
			if m.endpoints != before || m.state != Connecting {
				t.Fatal("unknown alert changed state")
			}
			want := 0
			if literal != "55000000351203041d" {
				want = 1
			}
			if len(m.out) != want {
				t.Errorf("alert %s: diagnostics=%d want %d", literal, len(m.out), want)
			}
		}
	}
}
