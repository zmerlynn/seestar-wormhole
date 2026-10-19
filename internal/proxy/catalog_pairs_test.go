package proxy

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type pairAllocationCloud struct {
	simulatedCloud
	t            *testing.T
	list, detail string
}

func (c *pairAllocationCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	body := ""
	switch r.URL.Path {
	case "/v1/my-device":
		body = c.list
		if r.URL.Query().Get("current") != "1" {
			return nil, errors.New("unexpected catalog page")
		}
	case "/v1/my-device/detail":
		c.details.Add(1)
		body = c.detail
	}
	if r.URL.Path == "/v1/my-device/detail" || r.URL.Path == "/v1/device-p2p/online-link" {
		if r.URL.Query().Get("deviceSn") != "sim" || r.URL.Query().Get("deviceModel") != "S50 Pro" {
			c.t.Error("request did not use selected catalog pair")
		}
	}
	if body != "" {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return c.simulatedCloud.RoundTrip(r)
}

func TestCatalogPairAllocationBoundary(t *testing.T) {
	// Opposite duplicate status orders must give the same decision from detail.
	for _, catalog := range []struct {
		list  string
		input cloudhttp.Device
	}{
		{`{"data":{"total":2,"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"},{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"second"}]}}`, cloudhttp.Device{Model: "S50 Pro"}},
		{`{"data":{"total":2,"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"first"},{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"second"}]}}`, cloudhttp.Device{Model: "S50 Pro"}},
		{`{"data":{"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"unproven"}]}}`, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}},
	} {
		for _, tc := range []struct {
			name, detail string
			want         error
		}{
			{"online", `{"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`, nil},
			{"offline", `{"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"synthetic"}}`, session.ErrTransport},
			{"occupied", `{"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"synthetic"}}`, session.ErrBusy},
			{"wrong serial", `{"data":{"deviceSn":"private-other","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`, session.ErrUncertain},
			{"wrong model", `{"data":{"deviceSn":"sim","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}}`, session.ErrUncertain},
			{"coded busy wrong serial", `{"code":300404,"data":{"deviceSn":"private-other","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`, session.ErrUncertain},
			{"coded offline wrong model", `{"code":300406,"data":{"deviceSn":"sim","deviceModel":"S30","deviceStatus":2,"ssid":"synthetic"}}`, session.ErrUncertain},
			{"both wrong", `{"data":{"deviceSn":"private-other","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}}`, session.ErrUncertain},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tr := &pairAllocationCloud{t: t, list: catalog.list, detail: tc.detail}
				var logs bytes.Buffer
				c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				d, err := c.SelectDevice(t.Context(), catalog.input)
				if err != nil {
					t.Fatal(err)
				}
				a := allocator{c, d}
				lease, err := a.Allocate(t.Context())
				if !errors.Is(err, tc.want) {
					t.Fatalf("allocation=%v, want %v", err, tc.want)
				}
				if tc.want == nil {
					if lease == nil {
						t.Fatal("missing lease for online detail")
					}
					if err := lease.Stop(t.Context()); err != nil {
						t.Fatal(err)
					}
					if tr.allocs.Load() != 1 || tr.stops.Load() != 1 {
						t.Fatal("online detail did not allocate and release once")
					}
				} else if lease != nil || tr.allocs.Load() != 0 || tr.stops.Load() != 0 {
					t.Fatal("refused detail reached allocation")
				}
				if tr.details.Load() != 1 {
					t.Fatal("detail was not authoritative")
				}
				if strings.Contains(logs.String(), "private-other") {
					t.Fatal("detail serial escaped logs")
				}
			})
		}
	}
}
