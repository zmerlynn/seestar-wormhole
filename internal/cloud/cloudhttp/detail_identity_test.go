package cloudhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestDetailIdentityPrecedesUnavailableStatus(t *testing.T) {
	for _, lan := range []bool{false, true} {
		for _, status := range []int{0, 2, 4} {
			for _, identity := range []string{"serial", "model", "matching"} {
				t.Run(fmt.Sprintf("lan=%v/status=%d/%s", lan, status, identity), func(t *testing.T) {
					serial, model := "serial", "S50 Pro"
					if identity == "serial" {
						serial = "wrong-device"
					}
					if identity == "model" {
						model = "wrong-model"
					}
					body := fmt.Sprintf(`{"code":200,"data":{"deviceSn":%q,"deviceModel":%q,"deviceStatus":%d,"ssid":"synthetic-wifi"}}`, serial, model, status)
					base, _ := url.Parse("https://fixture.invalid")
					c := &Client{base: base, timeout: time.Second, transport: transportFunc(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
					})}
					d := Device{Serial: "serial", Model: "S50 Pro"}
					var err error
					if lan {
						_, err = c.DetailForLAN(t.Context(), d)
					} else {
						_, err = c.Detail(t.Context(), d)
					}
					if identity != "matching" {
						if !errors.Is(err, cloud.ErrInvalid) || errors.Is(err, cloud.ErrBusy) || errors.Is(err, cloud.ErrOffline) {
							t.Fatalf("wrong identity status=%d: err=%v, want invalid without busy/offline", status, err)
						}
					} else {
						want := cloud.ErrOffline
						if status == 4 {
							want = cloud.ErrBusy
						}
						if !errors.Is(err, want) {
							t.Fatalf("matching identity err=%v want %v", err, want)
						}
					}
				})
			}
		}
	}
}
