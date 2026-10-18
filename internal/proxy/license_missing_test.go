package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestCloudLicenseMissingStopsWithoutRedial(t *testing.T) {
	for _, tc := range []struct{ name, member string }{
		{"missing", ""}, {"null", `"sdkLicenseKey":null,`},
		{"empty", `"sdkLicenseKey":"",`}, {"whitespace", `"sdkLicenseKey":" \t\n",`},
	} {
		for _, lazy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/lazy=%v", tc.name, lazy), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var logs statsLogBuffer
					logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
					peer := &r3Cloud{faultPath: "/v1/device-p2p/online-link", faultStatus: 200,
						faultBody: `{"code":200,"data":{"conId":"r3-1","deviceUid":"PRIVATE_UID",` + tc.member + `"username":"PRIVATE_USER","password":"PRIVATE_PASSWORD"},"msg":"PRIVATE_MESSAGE"}`}
					client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer, Logger: diagnostic.Lifecycle(logger)})
					if err != nil {
						t.Fatal(err)
					}
					cfg := Config{Logger: logger}
					if lazy {
						cfg.Idle = time.Hour
					}
					s, err := start(t.Context(), cfg, func(s *Session) sessionedge.Factory {
						return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) {
							t.Error("missing license reached the license edge")
							return iotc.License{}, session.ErrTransport
						}}
					})
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					for range 2 {
						_, err := s.DialPort(t.Context(), 445)
						if p, r := diagnostic.Fields(err, ""); p != "cloud" || r != "cloud_license_missing" || !(errors.Is(err, session.ErrUncertain) || errors.Is(err, session.ErrStop)) {
							t.Fatalf("got %s/%s (%v), want blocking cloud/cloud_license_missing", p, r, err)
						}
						<-time.After(20 * time.Minute)
						synctest.Wait()
					}
					peer.mu.Lock()
					if peer.allocations != 1 || peer.stops["r3-1"] != 1 || len(peer.violations) != 0 {
						t.Errorf("allocations=%d stops=%v violations=%v, want one allocation and stop", peer.allocations, peer.stops, peer.violations)
					}
					peer.mu.Unlock()
					data := logs.take()
					var failures int
					for _, r := range loggingRecords(t, bytes.NewBuffer(data)) {
						if r["msg"] == "layer_failed" {
							failures++
							if r["phase"] != "cloud" || r["reason"] != "cloud_license_missing" || r["level"] != "ERROR" || !strings.Contains(r["hint"].(string), "service-side change, not your configuration") {
								t.Errorf("unexpected failure: %v", r)
							}
						}
					}
					if failures != 1 || strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), `"msg":"session_unavailable"`) {
						t.Errorf("want one sanitized failure and no outage reminders before first readiness: %s", data)
					}
				})
			})
		}
	}
}

func TestLicenseMissingOutageReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, _, raw := cloud.ParseOnline([]byte(`{"data":{"conId":"id","deviceUid":"uid","username":"user","password":"pass"}}`))
		var logs statsLogBuffer
		f := &busyFactory{channels: make(chan *busyChannels, 8)}
		attempts := 0
		f.allocate = func(context.Context) (sessionedge.Lease, error) {
			attempts++
			if attempts == 1 {
				return f, nil
			}
			return nil, cloudTerminal(raw)
		}
		s, err := startWithRNG(t.Context(), Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		<-time.After(time.Second)
		synctest.Wait()
		logs.take()
		<-time.After(5 * time.Minute)
		synctest.Wait()
		records := loggingRecords(t, bytes.NewBuffer(logs.take()))
		if len(records) != 1 || records[0]["msg"] != "session_unavailable" || records[0]["reason"] != "cloud_license_missing" || attempts != 2 {
			t.Errorf("records=%v attempts=%d, want cloud_license_missing reminder without retry", records, attempts)
		}
	})
}
