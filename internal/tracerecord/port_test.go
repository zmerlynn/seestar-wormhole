package tracerecord

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

func TestUnconfiguredDestinationPortIsAliased(t *testing.T) {
	s := new(memorySink)
	w := New(s, SizeCap, func() time.Time { return time.Unix(0, 0) })
	w.Record("tunnel", 1, 0, p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 54321}, []p2ptunnel.Action{{Kind: p2ptunnel.Opened, Port: 54321}})
	w.Close()
	rr, err := Read(bytes.NewReader(s.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rr) != 2 {
		t.Fatalf("records=%d want 2", len(rr))
	}
	var record struct {
		Input   map[string]any
		Outputs []map[string]any
	}
	line := bytes.Split(s.Bytes(), []byte{'\n'})[0]
	if err := json.Unmarshal(line, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if rr[0].Input.Port != 0 || rr[0].Outputs[0].Port != 0 {
		t.Fatalf("raw unconfigured port retained: %s", line)
	}
	if record.Input["port_alias"] != "port1" || record.Outputs[0]["port_alias"] != "port1" {
		t.Fatalf("port equality lost: %s", line)
	}
}

func TestMappedDevicePortsAndOtherPortAliases(t *testing.T) {
	w := &Writer{peers: make(map[string]string)}
	w.SetServicePorts([]uint16{4700, 4801})
	for _, port := range []uint16{4700, 4801} {
		got := w.summarize(p2ptunnel.Event{Kind: p2ptunnel.Open, Port: port})
		if got.Port != port || got.PortAlias != "" {
			t.Fatalf("mapped destination %d: %+v", port, got)
		}
		got = w.summarize(p2ptunnel.Action{Kind: p2ptunnel.Opened, Port: port})
		if got.Port != port || got.PortAlias != "" {
			t.Fatalf("mapped action destination %d: %+v", port, got)
		}
	}
	for _, port := range []uint16{54321, 4700, 4801, 4445} {
		peer := netip.AddrPortFrom(netip.MustParseAddr("192.0.2.7"), port)
		got := w.summarize(iotc.Event{Link: iotc.Link{Address: peer}})
		edge := w.summarize(EdgeEvent{Peer: peer})
		if got.Port != 0 || got.PortAlias == "" || edge.Port != 0 || edge.PortAlias != got.PortAlias {
			t.Fatalf("transport/client port %d: %+v / %+v", port, got, edge)
		}
		if port == 54321 || port == 4445 {
			destination := w.summarize(p2ptunnel.Event{Kind: p2ptunnel.Open, Port: port})
			if destination.Port != 0 || destination.PortAlias != got.PortAlias {
				t.Fatalf("unmapped destination %d: %+v", port, destination)
			}
		}
	}
	// A changed NAT port keeps the IP alias but receives its own port alias.
	first := w.summarize(EdgeEvent{Peer: netip.MustParseAddrPort("192.0.2.7:51000")})
	second := w.summarize(EdgeEvent{Peer: netip.MustParseAddrPort("192.0.2.7:51001")})
	if first.Peer != second.Peer || first.PortAlias == second.PortAlias {
		t.Fatalf("NAT mapping: %+v / %+v", first, second)
	}
	for port := uint16(1); port < 256; port++ {
		w.portAlias(port)
	}
	if len(w.ports) != 128 || w.portAlias(65535) != "portOverflow" {
		t.Fatalf("unbounded aliases: %d", len(w.ports))
	}
}

func TestReplayPortAliasesPreserveEquality(t *testing.T) {
	// Another instance introduced earlier aliases. Hydration uses arbitrary
	// synthetic port numbers rather than accidentally using the alias ordinal.
	records := []Record{
		{Version: 1, Record: "step", Sequence: 1, Machine: "tunnel", Instance: 7, Input: Summary{Kind: uint8(p2ptunnel.Open), PortAlias: "port4"}, Outputs: []Summary{{Kind: uint8(p2ptunnel.Opened), PortAlias: "port4"}}},
		{Version: 1, Record: "step", Sequence: 2, Machine: "tunnel", Instance: 7, Input: Summary{Kind: uint8(p2ptunnel.Open), PortAlias: "port5"}, Outputs: []Summary{{Kind: uint8(p2ptunnel.Opened), PortAlias: "port5"}}},
		{Version: 1, Record: "end", Sequence: 3},
	}
	hydrate := func(r Record) (p2ptunnel.Event, error) {
		port := uint16(54321)
		if r.Input.PortAlias == "port5" {
			port = 54322
		}
		return p2ptunnel.Event{Kind: p2ptunnel.Open, Port: port}, nil
	}
	step := func(e p2ptunnel.Event, _ core.Instant) []p2ptunnel.Action {
		return []p2ptunnel.Action{{Kind: p2ptunnel.Opened, Port: e.Port}}
	}
	if err := Replay(records, "tunnel", 7, hydrate, step); err != nil {
		t.Fatal(err)
	}
	records[1].Outputs[0].PortAlias = "port4"
	if err := Replay(records, "tunnel", 7, hydrate, step); err == nil {
		t.Fatal("different ports incorrectly compared equal")
	}
	// Two synthetic ports must not map to the same recorded alias, even when
	// the second locally generated alias happens to equal the recorded name.
	for i := range records[:2] {
		records[i].Input.PortAlias = "port2"
		records[i].Outputs[0].PortAlias = "port2"
	}
	distinct := func(r Record) (p2ptunnel.Event, error) {
		return p2ptunnel.Event{Kind: p2ptunnel.Open, Port: uint16(54320 + r.Sequence)}, nil
	}
	if err := Replay(records, "tunnel", 7, distinct, step); err == nil {
		t.Fatal("distinct synthetic ports collapsed to one alias")
	}
	records[1].Outputs[0].PortAlias = "portOverflow"
	called := false
	if err := Replay(records, "tunnel", 7, hydrate, func(e p2ptunnel.Event, now core.Instant) []p2ptunnel.Action { called = true; return nil }); err == nil || called {
		t.Fatal("overflow recording executed")
	}
}
