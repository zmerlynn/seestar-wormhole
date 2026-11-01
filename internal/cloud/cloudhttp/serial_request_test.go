package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestDetailInputSerial(t *testing.T) {
	for _, tc := range cloudSerialCases() {
		if tc.valid {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			serials := []string{tc.serial}
			if tc.name == "0x80" {
				serials = append(serials, "s\x80") // Also reject an invalid UTF-8 byte before I/O.
			}
			for _, serial := range serials {
				for _, operation := range []string{"detail", "LAN detail", "acquire"} {
					t.Run(operation, func(t *testing.T) {
						var logs bytes.Buffer
						f := &selectionCloud{detailBody: cloudSerialBody(t, tc.serial, false)}
						c := selectionClient(t, f, &logs, slog.LevelInfo)
						d := Device{Serial: serial, Model: "S30"}
						var err error
						switch operation {
						case "detail":
							_, err = c.Detail(t.Context(), d)
						case "LAN detail":
							_, err = c.DetailForLAN(t.Context(), d)
						case "acquire":
							var lease *Lease
							lease, err = c.Acquire(t.Context(), d)
							if lease != nil {
								defer lease.Close()
								t.Error("invalid serial acquired a lease")
							}
						}
						if !errors.Is(err, cloud.ErrConfig) || len(f.queries) != 0 {
							t.Fatalf("invalid serial reached transport: err=%v calls=%d", err, len(f.queries))
						}
					})
				}
			}
		})
	}
}

func TestSerialRequestVerbatim(t *testing.T) {
	for _, serial := range []string{"!", "!" + strings.Repeat("s", 62) + "~", strings.Repeat("\"\\", 32)} {
		// Models retain the independent generic input bound, including Unicode and spaces.
		model := " \u0080" + strings.Repeat("m", 4093)
		detailBody, err := json.Marshal(map[string]any{"data": map[string]any{"deviceSn": serial, "deviceModel": model, "deviceStatus": 1, "ssid": "synthetic"}})
		if err != nil {
			t.Fatal(err)
		}
		details, allocations, stops := 0, 0, 0
		c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			body := `{"data":null}`
			switch r.URL.Path {
			case "/v1/my-device/detail":
				details++
				body = string(detailBody)
			case "/v1/device-p2p/online-link":
				allocations++
				body = online
			case "/v1/device-p2p/stop":
				stops++
			default:
				t.Errorf("unexpected route %q", r.URL.Path)
			}
			if r.URL.Path != "/v1/device-p2p/stop" && (r.URL.Query().Get("deviceSn") != serial || r.URL.Query().Get("deviceModel") != model) {
				t.Error("request changed identity")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		d := Device{Serial: serial, Model: model}
		for _, request := range []func() (cloud.Detail, error){
			func() (cloud.Detail, error) { return c.Detail(t.Context(), d) },
			func() (cloud.Detail, error) { return c.DetailForLAN(t.Context(), d) },
		} {
			got, err := request()
			if err != nil || got.Serial() != serial || got.Model() != model {
				t.Fatalf("valid detail changed: %v", err)
			}
		}
		lease, err := c.Acquire(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		if details != 2 || allocations != 1 || stops != 1 {
			t.Fatalf("lifecycle calls: detail=%d acquire=%d stop=%d", details, allocations, stops)
		}
	}
}
