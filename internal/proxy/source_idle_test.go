package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type sourceTrackingCloud struct {
	simulatedCloud
	ids chan string
}

func (c *sourceTrackingCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/device-p2p/online-link" {
		c.ids <- r.URL.Query().Get("srcId")
	}
	return c.simulatedCloud.RoundTrip(r)
}
func TestIdleRedialReusesEphemeralIdentity(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	synctest.Test(t, func(t *testing.T) {
		tr := &sourceTrackingCloud{ids: make(chan string, 2)}
		var logs bytes.Buffer
		c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var trace []string
		s, err := start(ctx, Config{Idle: time.Second}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var first string
		for i := range 2 {
			conn, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			conn.Write([]byte(rpcLine(i+1, 0)))
			b := make([]byte, 256)
			if _, err = conn.Read(b); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			conn.Close()
			synctest.Wait()
			time.Sleep(2 * time.Second)
			synctest.Wait()
			id := <-tr.ids
			if id == "" {
				t.Fatal("empty source id")
			}
			if i == 0 {
				first = id
			} else if id != first {
				t.Fatal("idle redial changed identity")
			}
			if tr.stops.Load() != int32(i+1) {
				t.Fatal("idle release missing")
			}
		}
		if strings.Count(logs.String(), "installation_id_ephemeral") != 1 {
			t.Fatal("ephemeral notice missing or repeated")
		}
		if strings.Contains(logs.String(), first) {
			t.Fatal("installation id logged")
		}
	})
}
