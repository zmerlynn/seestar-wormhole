package proxy

import (
	"bytes"
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/dtls"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Coalesce opaque early application records around the server's authenticated
// Finished flight at the actual IOTC -> packet-adapter boundary.
type e2eDTLSWire struct {
	*simulatedWire
	injected bool
}

func (w *e2eDTLSWire) Run(ctx context.Context, actions <-chan iotc.Action, emit func(iotc.Event) bool) {
	w.simulatedWire.Run(ctx, actions, func(e iotc.Event) bool {
		if e.Kind != iotc.Wire || w.injected {
			return emit(e)
		}
		key := iotc.ScrambleKey([16]byte{})
		q, err := iotc.DecodeLegacy(e.Data, key)
		if err != nil || q.Opcode != 0x0408 {
			return emit(e)
		}
		_, data, err := iotc.ParseData(q)
		if err != nil {
			w.t.Error(err)
			return false
		}
		records, err := recordlayer.UnpackDatagram(data)
		if err != nil {
			w.t.Error(err)
			return false
		}
		for _, record := range records {
			var h recordlayer.Header
			if err := h.Unmarshal(record); err != nil {
				w.t.Error(err)
				return false
			}
			if h.ContentType != protocol.ContentTypeHandshake || h.Epoch != 1 {
				continue
			}
			// Literal RFC 6347 envelope; the payload deliberately has no valid
			// authentication tag. Admission must discard it before Pion sees it.
			early := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1, 42}
			q.Body = bytes.Join([][]byte{q.Body[:len(q.Body)-len(data)], early, data, early}, nil)
			e.Data, err = iotc.EncodeLegacy(q, key)
			if err != nil {
				w.t.Error(err)
				return false
			}
			w.injected = true
			break
		}
		return emit(e)
	})
}

func TestE2EComposedDTLSHandshakeAdmission(t *testing.T) {

	for _, suite := range []uint16{dtls.CCAC, dtls.PSKAES128GCM} {
		t.Run(map[uint16]string{dtls.CCAC: "ccac", dtls.PSKAES128GCM: "aes_gcm"}[suite], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cloud := &simulatedCloud{}
				client, err := newFixtureClient(t, cloudhttp.Config{Transport: cloud})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				var trace []string
				wire := &e2eDTLSWire{simulatedWire: newSimulatedWire(t, &trace)}
				wire.suite = suite
				stacks := make(chan *stack, 1)
				s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
					return &trackingFactory{factory: &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return wire }}, stacks: stacks}
				})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatalf("composed handshake/admission: %v", err)
				}
				defer c.Close()
				x := <-stacks
				if got := x.secure.Profile(); got != (dtls.Profile{Version: dtls.Version12, Suite: suite}) {
					t.Fatalf("authenticated profile = %v, want suite %x", got, suite)
				}
				if err := exchange(c.(*stream), 1, 0); err != nil {
					t.Fatalf("post-handshake RPC: %v", err)
				}
				c.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if !wire.injected {
					t.Fatal("fixture never coalesced early application with Finished")
				}
				if cloud.allocs.Load() != 1 || cloud.stops.Load() != 1 {
					t.Fatalf("allocation/stop = %d/%d, want 1/1", cloud.allocs.Load(), cloud.stops.Load())
				}
				assertStackWorkersExited(t, x)
				assertDTLSWorkersExited(t, x)
			})
		})
	}
}
