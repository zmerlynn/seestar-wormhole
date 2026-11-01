package cloudhttp

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestSelectionDetailFallback(t *testing.T) {
	const serial = "serial-sentinel-secret-1234"
	const good = `{"code":200,"data":{"deviceSn":"serial-sentinel-secret-1234","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
	for catalogIndex, catalog := range []string{emptyDevices, pageA1} {
		for _, tc := range []struct {
			name, body string
			input      Device
			ok         bool
			detail     bool
		}{
			{"matching", good, Device{serial, "S50 Pro"}, true, true},
			{"wrong serial", strings.Replace(good, serial, "other-serial-sentinel-5678", 1), Device{serial, "S50 Pro"}, false, true},
			{"wrong model", strings.Replace(good, "S50 Pro", "S30", 1), Device{serial, "S50 Pro"}, false, true},

			{"model required", good, Device{Serial: serial}, false, false},
			{"serial required", good, Device{Model: "S50 Pro"}, false, false},
		} {
			t.Run(fmt.Sprintf("%s/catalog-%d", tc.name, catalogIndex), func(t *testing.T) {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: []string{catalog}, detailBody: tc.body}
				c := selectionClient(t, f, &logs, slog.LevelInfo)
				got, err := c.SelectDevice(t.Context(), tc.input)
				// A catalog with one different device can still select it without a serial.
				wantOK := tc.ok || (catalog == pageA1 && tc.input.Serial == "")
				if wantOK {
					want := tc.input
					if tc.input.Serial == "" {
						want = Device{"private-a", "S50 Pro"}
					}
					if err != nil || got != want {
						t.Fatalf("got=%v error=%v", got, err)
					}
					if !strings.Contains(logs.String(), `"msg":"device_selected"`) {
						t.Fatal("selection not logged")
					}
				} else if !errors.Is(err, cloud.ErrConfig) || got != (Device{}) || strings.Contains(logs.String(), `"msg":"device_selected"`) {
					t.Fatalf("failed detail selected: got=%v error=%v", got, err)
				}
				var details int
				for i, path := range f.paths {
					if path == "/v1/my-device/detail" {
						details++
						q, _ := url.ParseQuery(f.queries[i])
						if q.Get("deviceSn") != serial || q.Get("deviceModel") != "S50 Pro" || q.Get("onlyQueryOwner") != "false" {
							t.Fatal("wrong fallback detail query")
						}
					} else if path != "/v1/user/login" && path != "/v1/user/current-profile" && path != "/v1/my-device" {
						t.Fatal("selection allocated a session")
					}
				}
				wantDetails := 0
				if tc.detail {
					wantDetails = 1
				}
				if details != wantDetails {
					t.Fatalf("detail requests=%d want=%d", details, wantDetails)
				}
				assertSelectionSecrets(t, logs.String())
			})
		}
	}
}

func TestDetailIncludesShared(t *testing.T) {
	for _, lan := range []bool{false, true} {
		var logs bytes.Buffer
		f := &selectionCloud{detailBody: detail}
		c := selectionClient(t, f, &logs, slog.LevelInfo)
		var err error
		if lan {
			_, err = c.DetailForLAN(t.Context(), Device{"serial", "S50 Pro"})
		} else {
			_, err = c.Detail(t.Context(), Device{"serial", "S50 Pro"})
		}
		if err != nil {
			t.Fatal(err)
		}
		q, _ := url.ParseQuery(f.queries[len(f.queries)-1])
		if q.Get("onlyQueryOwner") != "false" {
			t.Fatal("detail omitted shared-device scope")
		}
	}
}
