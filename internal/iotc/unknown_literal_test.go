package iotc

import (
	"bytes"
	"testing"
)

// Synthetic decoded literals, independently transcribed from the audit's
// C07/C10/C12/C13 cases. Only framing/scrambling is applied by the test seam.
const unknownRecordLiteral = "51cc00000263000007000000"
const unknownControlLiteral = "04021d0000000000ffff000000000000"
const opaqueDataLiteral = "04021d0a0f00000008041280000000010c0000004433221188776655616263"
const opaqueKnockLiteral = "04021d002400000002043480000000004558414d504c455549443030303030303030303044332211887766550000000000080304"
const unknownTriggerLiteral = "55000000341201091d"
const unknownResultLiteral = "04021d00200000000d022400000000004558414d504c45554944303030303030303030307b0000004433221188776655"

// The mock authenticates only an Open requested through production ingress.
// Plaintext never goes to Wire. Token, attempt, endpoint, SID and size checks
// are exercised; separate AEAD tests exercise actual tags.
func unknownIngress(t *testing.T, m *Machine, l Link, typ byte, plain []byte) []Action {
	t.Helper()
	header := unhex("51cc0000020400000700000055000000")
	header[5] = typ
	if typ != 4 {
		header = header[:12]
	}
	put16(header[6:], uint16(len(header)-12+len(plain)+16))
	out := m.Step(Event{Kind: Wire, Attempt: m.cfg.Attempt, Link: l, Data: append(header, make([]byte, len(plain)+16)...)}, 0)
	for _, a := range out {
		if a.Kind == Open {
			return m.Step(Event{Kind: Decrypted, Attempt: m.cfg.Attempt, Token: a.Token, Data: plain}, 0)
		}
	}
	t.Fatal("fixture did not reach authenticated-completion seam")
	return nil
}

func unknownMachine(t *testing.T) (*Machine, Link) {
	t.Helper()
	m, l := layoutHelloMachine(t)
	l.Transport, l.ID = TCP, 1
	m.endpoints[0].link = l
	m.endpoints[0].stage, m.endpoints[0].sid = rendezvousPending, 0x55
	m.cache.Entries[0].Key = 3
	m.cache.Entries[0].CachedMarker = m.cache.Entries[0].Marker
	m.selection, m.selected = true, TCP
	return m, l
}

func TestUnknownLiteralClassification(t *testing.T) {
	t.Run("record", func(t *testing.T) {
		r, err := ParseRecord(unhex(unknownRecordLiteral))
		if err != nil || !r.Unknown() || r.Type != 99 || r.SID != 7 || len(r.Body) != 0 {
			t.Fatalf("bounded unknown record: type=%d SID=%d err=%v", r.Type, r.SID, err)
		}
	})
	t.Run("control", func(t *testing.T) {
		c, err := ParseControl(layoutPacket(t, unknownControlLiteral))
		if err != nil || !c.Unknown || c.UID != [20]byte{} || c.RID != [8]byte{} || c.Result != 0 {
			t.Fatalf("bounded unknown control interpreted/rejected: err=%v", err)
		}
	})
}

func TestUnknownLiteralOpaqueRoutes(t *testing.T) {
	t.Run("data", func(t *testing.T) {
		m := established(t, true)
		m.alive = 17
		out := layoutWire(t, m, m.path, unhex(opaqueDataLiteral))
		received := 0
		for _, a := range out {
			if a.Kind == Received {
				received++
				if !bytes.Equal(a.Data, []byte("abc")) {
					t.Fatalf("data=%x", a.Data)
				}
			}
		}
		if received != 1 || m.alive != 0 {
			t.Fatalf("received=%d alive=%d", received, m.alive)
		}
	})
	t.Run("knock", func(t *testing.T) {
		m, l := layoutKnockMachine(t)
		layoutWire(t, m, l, unhex(opaqueKnockLiteral))
		if m.State() != Established || !m.Direct() {
			t.Fatalf("opaque knock state=%v", m.State())
		}
	})
}

func TestUnknownLiteralAlertTrigger(t *testing.T) {
	m, l := unknownMachine(t)
	unknownIngress(t, m, l, 14, unhex(unknownTriggerLiteral))
	if m.State() != Closed {
		t.Fatalf("authenticated offline alert left state=%v", m.State())
	}
}

func TestUnknownLiteralRelayResult(t *testing.T) {
	m, l := unknownMachine(t)
	before := m.RelayProgress()
	unknownIngress(t, m, l, 4, unhex(unknownResultLiteral))
	if m.State() != Connecting || m.endpoints[0].stage != rendezvousPending || m.pathFailures() != 0 {
		t.Fatalf("unknown result manufactured outcome: state=%v reasons=%v", m.State(), m.pathFailures())
	}
	if m.RelayProgress() != before {
		t.Fatalf("unknown result counted as known: %+v", m.RelayProgress())
	}
}
