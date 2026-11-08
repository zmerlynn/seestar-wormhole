// Package proxytest is the simulated cloud and device that package proxy's
// tests run against, exported so that other packages' tests can compose a
// real proxy.Session with proxy.StartWithWire. It does not import package
// proxy, and no command imports it.
package proxytest

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// Device is the simulated telescope the Cloud describes.
var Device = cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}

// Verify stands in for IOTC license verification; the simulated wire needs
// no license.
func Verify(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }

// Cloud is a cloudhttp.Transport for the vendor cloud: login, profile, device
// detail, allocation and stop. It counts the calls it answers.
type Cloud struct {
	Allocs, Stops, Details atomic.Int32
	Logins                 atomic.Int32
	Busy                   bool // Device detail reports the device busy.
}

func (c *Cloud) RoundTrip(r *http.Request) (*http.Response, error) {
	var b string
	switch r.URL.Path {
	case "/v1/user/login":
		c.Logins.Add(1)
		b = `{"code":0,"data":{"accessToken":"sim-account-token"}}`
	case "/v1/user/current-profile":
		b = `{"code":0,"data":{"id":"sim-account"}}`
	case "/v1/my-device/detail":
		c.Details.Add(1)
		b = `{"code":200,"msg":"ok","data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
	case "/v1/device-p2p/online-link":
		c.Allocs.Add(1)
		b = `{"code":200,"msg":"ok","data":{"conId":"sim-allocation","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"sim-license","username":"sim-user","password":"sim-pass"}}`
	case "/v1/device-p2p/stop":
		c.Stops.Add(1)
		b = `{"code":200,"msg":"ok","data":null}`
	}
	if c.Busy && r.URL.Path == "/v1/my-device/detail" {
		b = `{"code":300404,"msg":"busy","data":null}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b))}, nil
}

type accountTransport struct{ cloudhttp.Transport }

func (f accountTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	switch r.URL.Path {
	case "/v1/user/login":
		body = `{"code":0,"data":{"accessToken":"fixture-token"}}`
	case "/v1/user/current-profile":
		body = `{"code":0,"data":{"id":"fixture-account"}}`
	default:
		return f.Transport.RoundTrip(r)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// NewClient returns a cloud client over cfg.Transport whose account login and
// profile are answered here, independently of the transport's failure routes.
// It sets fixture credentials in the test's environment.
func NewClient(t testing.TB, cfg cloudhttp.Config) (*cloudhttp.Client, error) {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	cfg.Transport = accountTransport{cfg.Transport}
	return cloudhttp.NewFromEnvironment(cfg)
}
