package cloudhttp

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestDetailPairLiterals(t *testing.T) {
	for _, body := range []string{
		`{"code":300404,"data":{"deviceSn":"private-b","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`,
		`{"code":300406,"data":{"deviceSn":"private-a","deviceModel":"S30","deviceStatus":2,"ssid":"synthetic"}}`,
		// Identity must survive unrelated record failures, including refusals.
		`{"code":300404,"data":{"deviceSn":"private-b","deviceModel":"S50 Pro","deviceStatus":1}}`,
		`{"code":300406,"data":{"deviceSn":"private-a","deviceModel":"S30","ssid":"synthetic"}}`,
		`{"code":300403,"data":{"deviceStatus":"private-secret","deviceSn":"private-b","deviceModel":"S50 Pro","ssid":"synthetic"}}`,
		`{"code":300405,"data":{"ssid":false,"deviceSn":"private-a","deviceModel":"S30","deviceStatus":1}}`,
		`{"data":{"deviceSn":"private-b","deviceModel":"S50 Pro","deviceStatus":1}}`,
		`{"data":{"deviceStatus":false,"deviceSn":"private-a","deviceModel":"S30","ssid":"synthetic"}}`,
		`{"data":{"deviceSn":"private-b","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`,
		`{"data":{"deviceSn":"private-a","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}}`,
		`{"data":{"deviceSn":"private-b","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}}`,
		`{"data":{"deviceSn":"private-a","deviceModel":"s50 pro","deviceStatus":2,"ssid":"synthetic"}}`,
		`{"data":{"deviceSn":"private-b","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"synthetic"}}`,
	} {
		for _, lan := range []bool{false, true} {
			var logs bytes.Buffer
			c, err := newAuthenticatedFixture(t, Config{Logger: redactionTestLogger(&logs, slog.LevelDebug), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v1/my-device/detail" || r.URL.Query().Get("deviceSn") != "private-a" || r.URL.Query().Get("deviceModel") != "S50 Pro" {
					t.Fatal("wrong detail pair query")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			var got cloud.Detail
			if lan {
				got, err = c.DetailForLAN(t.Context(), Device{"private-a", "S50 Pro"})
			} else {
				got, err = c.Detail(t.Context(), Device{"private-a", "S50 Pro"})
			}
			c.Close()
			phase, reason := diagnostic.Fields(err, "")
			if !errors.Is(err, cloud.ErrInvalid) || errors.Is(err, cloud.ErrBusy) || errors.Is(err, cloud.ErrOffline) || got != (cloud.Detail{}) || phase != "device_detail" || reason != "inconsistent server answer: detail identity differs from requested pair" {
				t.Fatalf("detail=%v error=%v phase=%s reason=%s, want inconsistent server answer", got, err, phase, reason)
			}
			diagnostic.Log(c.logger, "detail refused", "device_detail", err)
			if !strings.Contains(logs.String(), "inconsistent server answer") {
				t.Fatal("missing inconsistency diagnostic")
			}
			assertCatalogPagePrivacy(t, logs.String()+err.Error())
		}
	}
}
