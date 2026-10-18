package cloudhttp

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestHTTP400OccupiedDataIsNotVendorBusyCode(t *testing.T) {
	for _, path := range []string{"detail", "online-link"} {
		for _, code := range []string{"200", "401", "9876", "300403", "300404"} {
			t.Run(path+"/"+code, func(t *testing.T) {
				base, _ := url.Parse("https://fixture.invalid")
				body := `{"code":` + code + `,"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"synthetic-wifi"}}`
				c := &Client{base: base, timeout: time.Second, stopTimeout: time.Second, transport: transportFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				d := Device{Serial: "serial", Model: "S50 Pro"}
				var err error
				if path == "detail" {
					_, err = c.Detail(t.Context(), d)
				} else {
					_, err = c.Acquire(t.Context(), d)
				}
				want := cloud.Denied
				if code == "300403" || code == "300404" {
					want = cloud.Busy
				}
				if got := cloud.Disposition(err); got != want {
					t.Fatalf("HTTP 400 envelope code=%s rank=%v want %v", code, got, want)
				}
				if want == cloud.Denied && !errors.Is(err, cloud.ErrRejected) {
					t.Fatalf("HTTP 400 lost header denial: %v", err)
				}
			})
		}
	}
}
