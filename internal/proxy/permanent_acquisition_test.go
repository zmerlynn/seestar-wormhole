package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestPermanentAcquisitionWithBusyStop(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"rejected", `{"code":300405,"data":{"conId":"r3-1"}}`, 200},
		{"license-missing", `{"code":200,"data":{"conId":"r3-1","deviceUid":"fixture","username":"fixture","password":"fixture"}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				peer := &r3Cloud{faultPath: "/v1/device-p2p/online-link", faultStatus: tc.status, faultBody: tc.body, stopBody: `{"code":300403,"data":null}`}
				client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				s, err := start(context.Background(), Config{}, func(s *Session) sessionedge.Factory {
					return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
				})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				synctest.Wait()
				<-time.After(3 * time.Minute)
				synctest.Wait()
				peer.mu.Lock()
				calls := peer.allocations
				stops := 0
				for _, count := range peer.stops {
					stops += count
				}
				peer.mu.Unlock()
				if stops != calls {
					t.Fatalf("allocations=%d internal stops=%d, want one stop per allocation", calls, stops)
				}
				if err := s.Close(); !errors.Is(err, session.ErrStop) {
					t.Fatalf("lost failed cleanup on shutdown: %v", err)
				}
				s.mu.Lock()
				_, reason := diagnostic.Fields(s.terminalFailure, "")
				s.mu.Unlock()
				if calls != 1 {
					t.Fatalf("permanent acquisition retried: allocations=%d reason=%s", calls, reason)
				}
			})
		})
	}
}

func TestTransportAcquisitionWithBlockingStop(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"rejected", `{"code":300405,"data":null}`},
		{"malformed", `{"code":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				peer := &r3Cloud{faultPath: "/v1/device-p2p/online-link", faultStatus: 200, bodyFault: true, stopBody: tc.body}
				client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				s, err := start(context.Background(), Config{}, func(s *Session) sessionedge.Factory {
					return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
				})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				synctest.Wait()
				<-time.After(59 * time.Second)
				synctest.Wait()
				peer.mu.Lock()
				early := peer.allocations
				peer.mu.Unlock()
				if early != 1 {
					t.Fatalf("replacement before recovery floor: allocations=%d", early)
				}
				<-time.After(2*time.Minute + time.Second)
				synctest.Wait()
				peer.mu.Lock()
				calls := peer.allocations
				stops := 0
				for _, count := range peer.stops {
					stops += count
				}
				peer.mu.Unlock()
				if stops != calls {
					t.Fatalf("allocations=%d internal stops=%d, want one stop per allocation", calls, stops)
				}
				if err := s.Close(); !errors.Is(err, session.ErrStop) {
					t.Fatalf("lost failed cleanup on shutdown: %v", err)
				}
				s.mu.Lock()
				_, reason := diagnostic.Fields(s.terminalFailure, "")
				s.mu.Unlock()
				if calls != 4 {
					t.Fatalf("ownership-only failed stop did not recover: allocations=%d reason=%s", calls, reason)
				}
			})
		})
	}
}
