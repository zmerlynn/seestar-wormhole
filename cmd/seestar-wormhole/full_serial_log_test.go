package main

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

type serialLogSink struct{ bytes.Buffer }

func (*serialLogSink) Close() error { return nil }

func TestFullSerialBoundedLog(t *testing.T) {
	// Four display bytes and five JSON bytes per input byte is the worst
	// expansion for accepted serials (including non-UTF-8 bytes).
	serial := strings.Repeat("\x01", 4096)
	escaped := strings.Repeat(`\x01`, 4096)
	if !cloudhttp.ValidSerial(serial) {
		t.Fatal("maximum serial rejected")
	}
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			sink := &serialLogSink{}
			queue := newQueuedLog(sink)
			logger := newLogger(format, queue)
			t.Setenv("SEESTAR_EMAIL", "synthetic@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "synthetic-password")
			encoded, _ := json.Marshal(serial)
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
			if err != nil {
				t.Fatal(err)
			}
			// The proxy regression separately checks the real ready observation. Use
			// its record fields here to require delivery through the command's sink.
			logger.Info("session_phase_complete", "phase", "ready", "layer", "tunnel", "reason", "", "retry_ms", 0, "local_clients", "forward", "telescope", d.DisplayName())
			queue.close()
			<-queue.done
			if queue.Dropped() != 0 {
				t.Fatalf("full serial dropped: %d", queue.Dropped())
			}
			selected, ready := 0, 0
			for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
				if len(line)+1 > maxLogLine {
					t.Fatalf("record bytes=%d exceeds sink", len(line)+1)
				}
				if strings.Contains(line, "device_selected") {
					selected++
					want := strconv.Quote(escaped)
					if format == "text" {
						want = "serial=" + escaped
					}
					if !strings.Contains(line, want) {
						t.Error("selected record lost full serial")
					}
					t.Logf("%s selected bytes=%d", format, len(line)+1)
				}
				if strings.Contains(line, "session_phase_complete") {
					ready++
					if !strings.Contains(line, strconv.Quote("S30 "+escaped)) {
						t.Error("ready record lost full serial")
					}
					t.Logf("%s ready bytes=%d", format, len(line)+1)
				}
			}
			if selected != 1 || ready != 1 {
				t.Fatalf("selected=%d ready=%d", selected, ready)
			}
		})
	}
}
