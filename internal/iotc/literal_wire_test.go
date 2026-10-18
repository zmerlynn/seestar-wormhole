package iotc

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"net/netip"
	"os"
	"testing"
)

func TestLiteralPacketBuilders(t *testing.T) {
	var fs []struct{ Name, Hex string }
	b, err := os.ReadFile("testdata/wire-literals.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fs, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	cfg := config()
	mat := material()
	packets := []Packet{masterQuery(cfg.UID, "test", mat), precheck(cfg.UID, mat), rendezvous(0x0240, cfg.UID, mat, 2, 0, false), rendezvous(0x020c, cfg.UID, mat, 2, 0, false), rendezvous(0x0241, cfg.UID, mat, 2, 0, false), rendezvous(0x0241, cfg.UID, mat, 2, 0, true), rendezvous(0x030f, cfg.UID, mat, 2, 0x2345, false)}
	for _, op := range []uint16{0x0401, 0x0402, 0x0403, 0x0404} {
		packets = append(packets, knock(op, cfg.UID, mat.RID))
	}
	for i, f := range fs {
		got, err := packets[i].Marshal()
		if err != nil || !bytes.Equal(got, unhex(f.Hex)) {
			t.Errorf("%s got %x want %s err %v", f.Name, got, f.Hex, err)
		}
	}
}

// These full AAD fixtures are literal transcriptions of record layouts, not
// output from RecordHeader. The inner fixtures are the independent
// eleven independent synthetic sender packets, not vendor captures.
func TestLiteralRecordAAD(t *testing.T) {
	var fs []struct{ Name, Hex, AAD string }
	b, err := os.ReadFile("testdata/wire-literals.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fs, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for j, f := range fs[:7] {
		t.Run(f.Name, func(t *testing.T) {
			m, _ := New(config())
			m.started = true
			m.state = Connecting
			m.material = material()
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			if j == 5 {
				l.Transport = TCP
			} // Exercise selected TCP here; other fixtures also cover selected UDP.
			idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
			m.cache.Entries[idx].Key = 3
			m.cache.Entries[idx].CachedMarker = Marker{10, 1}
			m.ivs[idx] = [12]byte{8}
			m.add(endpoint{link: l, connected: true, cache: idx, sid: 0x55})
			if j == 0 {
				m.query(0)
			} else if j == 1 {
				m.prepare(0)
			} else {
				if j == 5 {
					m.relaySelected = l
				}
				m.endpoints[0].relayID = 0x2345
				ops := []uint16{0x0240, 0x020c, 0x0241, 0x0241, 0x030f}
				m.control(0, ops[j-2])
			}
			if len(m.out) != 1 || m.out[0].Kind != Seal {
				t.Fatal("expected one seal")
			}
			a := m.out[0]
			want := unhex(f.Hex)
			if j == 0 {
				want = append(want, make([]byte, 256-len(want))...)
			}
			if !bytes.Equal(a.Data, want) || !bytes.Equal(a.AAD, unhex(f.AAD)) {
				t.Fatalf("plaintext=%x AAD=%x want plaintext=%x AAD=%s", a.Data, a.AAD, want, f.AAD)
			}
		})
	}
}
