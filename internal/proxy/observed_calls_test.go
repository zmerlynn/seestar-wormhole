package proxy

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type observedCallTransport struct {
	peer              r3Cloud
	path, body, fault string
}
type observedCallBody struct {
	io.ReadCloser
	closeErr   error
	panicClose bool
}

func (b observedCallBody) Close() error {
	if b.panicClose {
		panic("synthetic close failure")
	}
	_ = b.ReadCloser.Close()
	return b.closeErr
}
func (tr *observedCallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := tr.peer.RoundTrip(req)
	if req.URL.Path != tr.path {
		return resp, err
	}
	if tr.body != "" {
		resp.Body = io.NopCloser(strings.NewReader(tr.body))
	}
	switch tr.fault {
	case "roundtrip":
		return resp, io.ErrUnexpectedEOF
	case "read":
		resp.Body = io.NopCloser(io.MultiReader(resp.Body, r3ReadError{}))
	case "read-panic":
		resp.Body = io.NopCloser(io.MultiReader(resp.Body, observedPanicReader{}))
	case "close-panic":
		resp.Body = observedCallBody{ReadCloser: resp.Body, panicClose: true}
	case "close":
		resp.Body = observedCallBody{ReadCloser: resp.Body, closeErr: io.ErrUnexpectedEOF}
	}
	return resp, err
}

// Each completed body establishes an outcome before a later read/close fault.
// Literal semantic-invalid JSON distinguishes observed malformed input from a
// truncated body that never established a primary outcome.
func TestObservedOutcomesEveryCloudCall(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, malformed := range []bool{false, true} {
			for _, fault := range []string{"read", "close", "roundtrip"} {
				name := path + "/" + fault
				if malformed {
					name += "/malformed"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
					t.Setenv("SEESTAR_PASSWORD", "fixture-password")
					synctest.Test(t, func(t *testing.T) {
						tr := &observedCallTransport{path: path, fault: fault}
						if malformed {
							tr.body = `{"code":"invalid","data":{"conId":"r3-1"}}`
						}
						c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
						if err != nil {
							t.Fatal(err)
						}
						defer c.Close()
						err = observeCloudCall(t, c, path)
						if err == nil {
							t.Fatal("observed body failure disappeared")
						}
						if malformed {
							if !(errors.Is(err, cloud.ErrInvalid) || errors.Is(err, session.ErrUncertain) || errors.Is(err, session.ErrStop)) {
								t.Errorf("malformed + %s = %v, want blocking", fault, err)
							}
						} else if !(retryableCloud(err) || errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrReleaseAbandoned)) {
							t.Errorf("successful primary + %s = %v, want recoverable", fault, err)
						}
					})
				})
			}
		}
	}
}

func TestCompletedDeviceStatusOffline(t *testing.T) {
	for _, body := range []string{
		`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":0,"ssid":"synthetic-wifi"}}`,
		`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"synthetic-wifi"}}`,
	} {
		tr := &r3Cloud{faultPath: "/v1/my-device/detail", faultStatus: 200, faultBody: body}
		c := r3Client(t, tr, false)
		_, err := (&allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}).Allocate(t.Context())
		c.Close()
		if !errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrUncertain) {
			t.Errorf("completed device-status offline = %v, want transport", err)
		}
		if tr.allocations != 0 {
			t.Errorf("offline allocations=%d, want 0", tr.allocations)
		}
	}
}

type observedPanicReader struct{}

func (observedPanicReader) Read([]byte) (int, error) { panic("synthetic read failure") }

func TestInternalBodyFailuresBlockEveryCloudCall(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, fault := range []string{"read-panic", "close-panic"} {
			t.Run(path+"/"+fault, func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "fixture-password")
				synctest.Test(t, func(t *testing.T) {
					tr := &observedCallTransport{path: path, fault: fault}
					c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					err = observeCloudCall(t, c, path)
					if !(errors.Is(err, cloud.ErrInternal) || errors.Is(err, session.ErrUncertain) || errors.Is(err, session.ErrStop)) {
						t.Errorf("internal %s result=%v, want fatal blocking", fault, err)
					}
					if path == "/v1/device-p2p/online-link" && tr.peer.stops["r3-1"] != 1 {
						t.Errorf("stops after body panic=%v, want one stop for captured ID", tr.peer.stops)
					}
				})
			})
		}
	}
}

func observeCloudCall(t *testing.T, c *cloudhttp.Client, path string) error {
	t.Helper()
	a := allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
	switch path {
	case "/v1/user/login", "/v1/user/current-profile":
		return c.Login(t.Context())
	case "/v1/my-device":
		_, err := c.SelectDevice(t.Context(), cloudhttp.Device{})
		return err
	case "/v1/my-device/detail", "/v1/device-p2p/online-link":
		_, err := a.Allocate(t.Context())
		return err
	case "/v1/device-p2p/stop":
		l, err := a.Allocate(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return l.Stop(t.Context())
	}
	t.Fatalf("unknown test path %q", path)
	return nil
}

func TestCompletedMalformedBodyCloseEveryCloudCall(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "fixture-password")
			synctest.Test(t, func(t *testing.T) {
				tr := &observedCallTransport{path: path, body: `{"code":`, fault: "close"}
				c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				err = observeCloudCall(t, c, path)
				if !(errors.Is(err, cloud.ErrInvalid) || errors.Is(err, session.ErrUncertain) || errors.Is(err, session.ErrStop)) {
					t.Errorf("completed malformed JSON + Close error = %v, want blocking", err)
				}
			})
		})
	}
}

func TestReceivedDetailIdentityCannotBeHiddenByBodyFailure(t *testing.T) {
	for _, fault := range []string{"read", "close", "roundtrip"} {
		tr := &observedCallTransport{path: "/v1/my-device/detail", fault: fault, body: `{"code":200,"data":{"deviceSn":"other","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`}
		t.Setenv("SEESTAR_EMAIL", "")
		t.Setenv("SEESTAR_PASSWORD", "")

		c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		_, err = (&allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}).Allocate(t.Context())
		c.Close()
		if !errors.Is(err, session.ErrUncertain) {
			t.Errorf("received wrong device + %s = %v, want blocking", fault, err)
		}
	}
}

// Check disposition as well as the release marker: a blocking stop also retains
// abandonment, so that marker alone cannot prove an offline stop is recoverable.
func TestCompletedVendorOfflineAdapters(t *testing.T) {
	for _, path := range []string{"/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		t.Run(path, func(t *testing.T) {
			tr := &r3Cloud{faultPath: path, faultStatus: 200, faultBody: `{"code":300406,"data":{"conId":"r3-1"}}`}
			c := r3Client(t, tr, false)
			defer c.Close()
			err := observeCloudCall(t, c, path)
			if !errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrUncertain) || errors.Is(err, session.ErrStop) {
				t.Errorf("complete offline adapter outcome=%v, want recoverable transport", err)
			}
			if path == "/v1/device-p2p/stop" && !errors.Is(err, session.ErrReleaseAbandoned) {
				t.Errorf("offline stop lost owed-release marker: %v", err)
			}
		})
	}
}
