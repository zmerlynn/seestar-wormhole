package cloudhttp

import (
	"io"
	"net/http"
	"testing"
)

func TestLANDetailUsesSelectedIdentityWithoutAllocation(t *testing.T) {
	requests := 0
	c := setup(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/my-device/detail" || r.URL.Query().Get("deviceSn") != "serial" || r.URL.Query().Get("deviceModel") != "S50 Pro" {
			t.Errorf("unexpected LAN metadata request %s", r.URL.Path)
		}
		io.WriteString(w, `{"code":200,"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Real Name"}}`)
	})
	d, err := c.DetailForLAN(t.Context(), Device{Serial: "serial", Model: "S50 Pro"})
	if err != nil || d.Name() != "Real Name" || d.Serial() != "serial" || d.Model() != "S50 Pro" || requests != 1 {
		t.Fatalf("LAN identity name=%q requests=%d err=%v", d.Name(), requests, err)
	}
}
