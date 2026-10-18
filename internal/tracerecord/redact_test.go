package tracerecord

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"net/netip"
	"testing"
	"time"
)

func TestEveryMachineRedactsSecrets(t *testing.T) {
	secret := "seeded-password-key-PSK-token-UID-account-serial-19.876543-site-time-2031"
	payload := []byte(secret)
	credentials, err := cloud.EmailCredentials("private@example.invalid", "seeded-password-secret")
	if err != nil {
		t.Fatal(err)
	}
	var material iotc.Material
	copy(material.RID[:], payload)
	copy(material.MasterKey[:], payload)
	copy(material.MasterIV[:], payload)
	copy(material.Public[:], payload)
	token, err := cloud.ParseLogin(200, []byte(`{"code":200,"data":{"accessToken":"`+secret+`"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	peer := netip.MustParseAddrPort("192.0.2.73:4720")
	for _, tc := range []struct {
		name          string
		input, output any
	}{
		{"iotc", iotc.Event{Kind: iotc.MaterialReady, Material: material, Data: payload, Addresses: []netip.Addr{peer.Addr()}, Link: iotc.Link{Address: peer}}, []iotc.Action{{Kind: iotc.Seal, Data: payload, AAD: payload, Domain: secret, Reason: secret, Link: iotc.Link{Address: peer}}}},
		{"rdt", rdt.Event{Kind: rdt.Receive, Packet: rdt.Packet{Type: rdt.Data, Payload: payload}}, []rdt.Action{{Kind: rdt.Bytes, Data: payload, Reason: secret}}},
		{"tunnel", p2ptunnel.Event{Kind: p2ptunnel.LocalData, Data: payload}, []p2ptunnel.Action{{Kind: p2ptunnel.WriteLocal, Data: payload, Reason: secret}}},
		{"account", cloud.AccountEvent{Token: token, Err: errors.New(secret)}, []cloud.AccountAction{{Credentials: credentials, Token: token, Err: errors.New(secret)}}},
		{"allocation", EdgeEvent{Data: payload, Err: errors.New(secret)}, []EdgeEvent{{Data: payload, Err: errors.New(secret)}}},
		{"session", session.Event{Err: errors.New(secret)}, []session.Action{{Err: errors.New(secret)}}},
		{"dtls", EdgeEvent{Data: payload, Err: errors.New(secret)}, []EdgeEvent{{Data: payload, Err: errors.New(secret)}}},
		{"lan", EdgeEvent{Data: payload}, []EdgeEvent{{Data: payload}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := new(memorySink)
			w := New(sink, 4096, func() time.Time { return time.Unix(0, 0) })
			w.Record(tc.name, 1, 0, tc.input, tc.output)
			w.Close()
			for _, s := range []string{secret, string(payload[:8]), string(payload[:12]), string(payload[:16]), base64.StdEncoding.EncodeToString(payload[:16]), hex.EncodeToString(payload[:16]), "seeded-password-secret", "private@example.invalid", "192.0.2.73", base64.StdEncoding.EncodeToString(payload), hex.EncodeToString(payload)} {
				if bytes.Contains(sink.Bytes(), []byte(s)) {
					t.Fatalf("trace contains secret %q: %s", s, sink.Bytes())
				}
			}
			rr, err := Read(bytes.NewReader(sink.Bytes()))
			if err != nil || len(rr) != 2 || rr[0].Input.Unsupported || rr[0].Outputs[0].Unsupported {
				t.Fatalf("empty or unsupported trace: %s %v", sink.Bytes(), err)
			}
		})
	}
}

func TestStableBoundedPeerAliases(t *testing.T) {
	w := &Writer{peers: make(map[string]string)}
	a := netip.MustParseAddrPort("192.0.2.7:1234")
	if got := w.peer(a); got != "peer1" {
		t.Fatal(got)
	}
	if got := w.peer(netip.AddrPortFrom(a.Addr(), 5678)); got != "peer1" {
		t.Fatal(got)
	}
	for i := 0; i < 256; i++ {
		w.peer(netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 3, byte(i)}), 1))
	}
	if len(w.peers) != 128 {
		t.Fatalf("aliases %d", len(w.peers))
	}
}

type blockedSink struct {
	entered, release chan struct{}
	memorySink
}

func (s *blockedSink) Write(b []byte) (int, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.memorySink.Write(b)
}
func TestBlockedStorageDropsWithoutWaiting(t *testing.T) {
	s := &blockedSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := New(s, SizeCap, time.Now)
	w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
	<-s.entered
	for i := 0; i < 100; i++ {
		w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
	}
	if w.Dropped() == 0 {
		t.Fatal("full queue did not drop")
	}
	close(s.release)
	w.Close()
	rr, err := Read(bytes.NewReader(s.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if rr[len(rr)-1].Dropped != w.Dropped() {
		t.Fatal("final drop count missing")
	}
}

func TestDTLSScalarProjectionRejectsArbitraryOutcome(t *testing.T) {
	w := &Writer{peers: make(map[string]string)}
	got := w.summarize(EdgeEvent{Kind: 5, Length: 14, Outcome: "private-password-token-path"})
	if got.Length != 14 || got.Error != "operation_failed" {
		t.Fatalf("scalar projection: %+v", got)
	}
	for _, outcome := range []string{"none", "timeout", "canceled", "operation_failed"} {
		if got := w.summarize(EdgeEvent{Outcome: outcome}); got.Error != outcome {
			t.Fatalf("outcome %q: %+v", outcome, got)
		}
	}
}
