package main

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

func TestCloudSerialSink(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			sink := &serialLogSink{}
			queue := newQueuedLog(sink)
			t.Cleanup(func() { queue.close(); <-queue.done })
			logger := newLogger(format, queue)
			t.Setenv("SEESTAR_EMAIL", "synthetic@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "synthetic-password")
			// This identity used to be selected and expand beyond the 16 KiB sink.
			encoded, err := json.Marshal(strings.Repeat("\x01", 4096))
			if err != nil {
				t.Fatal(err)
			}
			c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Logger: logger, StateDir: t.TempDir(), Transport: choicesTransport(func(r *http.Request) (*http.Response, error) {
				var body string
				switch r.URL.Path {
				case "/v1/user/login":
					body = `{"data":{"accessToken":"synthetic-token"}}`
				case "/v1/user/current-profile":
					body = `{"data":{"id":"synthetic-profile"}}`
				case "/v1/my-device":
					body = `{"data":{"total":1,"items":[{"deviceSn":` + string(encoded) + `,"deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}]}}`
				default:
					t.Errorf("unexpected cloud route %q", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			d, err := c.SelectDevice(t.Context(), cloudhttp.Device{})
			if !errors.Is(err, cloud.ErrInvalid) || d != (cloudhttp.Device{}) {
				t.Errorf("invalid cloud identity selected: serialBytes=%d err=%v", len(d.Serial), err)
			}
			// Model the caller's success-only ready publication. Valid identities
			// traverse both records in TestFullSerialBoundedLog.
			if err == nil {
				logger.Info("session_phase_complete", "phase", "ready", "layer", "tunnel", "reason", "", "retry_ms", 0, "local_clients", "forward", "telescope", d.DisplayName())
			}
			queue.close()
			<-queue.done
			if queue.Dropped() != 0 {
				t.Errorf("cloud rejection dropped records: %d", queue.Dropped())
			}
			for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
				if len(line)+1 > 16<<10 {
					t.Error("record exceeds 16 KiB sink")
				}
				if strings.Contains(line, "device_selected") || strings.Contains(line, "session_phase_complete") {
					t.Error("invalid cloud identity published")
				}
			}
		})
	}
}
