package iotc

import "testing"

// Unknown alerts are counted only after nonce checking, without installing
// even an empty server SID (SHOULD).
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
			if m.Discards()[4] != uint64(want) || len(m.out) != 0 {
				t.Errorf("alert %s: discard count=%d want %d; actions=%v", literal, m.Discards()[4], want, m.out)
			}
		}
	}
}
