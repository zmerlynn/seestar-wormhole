package proxy

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type traceSink struct{ bytes.Buffer }

func (*traceSink) Close() error { return nil }

func TestComposedTraceRecordsAllDrivenLayers(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	synctest.Test(t, func(t *testing.T) {
		sink := new(traceSink)
		rec := tracerecord.New(sink, tracerecord.SizeCap, time.Now)
		cloud := &simulatedCloud{}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: cloud, Trace: rec})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var impairment []string
		s, err := start(ctx, Config{Trace: rec}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &impairment) }}
		})
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		c.Write([]byte(rpcLine(1, 0)))
		c.(interface{ CloseWrite() error }).CloseWrite()
		if _, err = io.ReadAll(c); err != nil {
			t.Fatal(err)
		}
		c.Close()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		rec.Close()
		rr, err := tracerecord.Read(bytes.NewReader(sink.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, r := range rr {
			seen[r.Machine] = true
			if r.Record == "step" && (r.Instance == 0 || r.Input.Unsupported) {
				t.Fatalf("invalid record %+v", r)
			}
		}
		for _, name := range []string{"iotc", "rdt", "tunnel", "dtls", "session", "account", "allocation"} {
			if !seen[name] {
				t.Fatalf("missing %s", name)
			}
		}
		for _, secret := range []string{"fixture@example.invalid", "fixture-password", "sim-account-token", "sim-allocation", "EXAMPLEUID0000000000", "sim-license", "sim-user", "sim-pass", "192.0.2."} {
			if strings.Contains(sink.String(), secret) {
				t.Fatalf("leaked %q", secret)
			}
		}
	})
}
