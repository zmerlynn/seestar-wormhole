package cloudhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

type selectionCloud struct {
	bodies     []string
	detailBody string
	sharedOnly bool
	status     int
	paths      []string
	queries    []string
}

func (f *selectionCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	f.paths = append(f.paths, r.URL.Path)
	f.queries = append(f.queries, r.URL.RawQuery)
	var b string
	switch r.URL.Path {
	case "/v1/user/login":
		b = `{"code":0,"data":{"accessToken":"token-sentinel-secret"}}`
	case "/v1/user/current-profile":
		b = `{"code":0,"data":{"id":"profile-sentinel"}}`
	case "/v1/my-device/detail":
		if f.detailBody == "" {
			return nil, errors.New("unexpected detail")
		}
		b = f.detailBody
	case "/v1/my-device":
		if r.Header.Get("Authorization") != "token-sentinel-secret" {
			return nil, errors.New("token-sentinel-secret: missing auth")
		}
		if len(f.bodies) == 0 {
			return nil, errors.New("serial-sentinel-secret: unexpected list")
		}
		if f.sharedOnly && r.URL.Query().Get("isContainShared") != "3" {
			b = emptyDevices
			break
		}
		b = f.bodies[0]
		f.bodies = f.bodies[1:]
	default:
		return nil, errors.New("serial-sentinel-secret: unexpected endpoint")
	}
	status := 200
	if r.URL.Path == "/v1/my-device" && f.status != 0 {
		status = f.status
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(b))}, nil
}

const oneDevice = `{"code":200,"msg":"token-sentinel-secret","data":{"items":[{"deviceSn":"serial-sentinel-secret-1234","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"synthetic-wifi"}],"total":1}}`
const twoDevices = `{"code":200,"msg":"token-sentinel-secret","data":{"items":[{"deviceSn":"serial-sentinel-secret-1234","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"other-serial-sentinel-5678","deviceModel":"S30","deviceStatus":4,"ssid":"synthetic-wifi"}],"total":2}}`
const emptyDevices = `{"code":200,"msg":"ok","data":{"items":[],"total":0}}`

func selectionClient(t *testing.T, f *selectionCloud, logs *bytes.Buffer, level slog.Level) *Client {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	c, err := NewFromEnvironment(Config{Transport: f, StateDir: t.TempDir(), Logger: redactionTestLogger(logs, level)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func assertSelectionSecrets(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{"token-sentinel-secret", "serial-sentinel-secret-1234", "other-serial-sentinel-5678", "fixture@example.invalid", "fixture-password", "profile-sentinel"} {
		if strings.Contains(s, secret) {
			t.Fatal("secret escaped selection diagnostics")
		}
	}
}
func TestDeviceSelection(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		input, want Device
		reason      string
		listed      bool
	}{
		{name: "default one", body: oneDevice, want: Device{"serial-sentinel-secret-1234", "S50 Pro"}, listed: true},
		{name: "no devices", body: emptyDevices, reason: "incomplete catalog", listed: true},
		{name: "nullable empty list", body: `{"code":200,"msg":"ok","data":{"items":null,"total":0}}`, reason: "incomplete catalog", listed: true},
		{name: "ambiguous", body: twoDevices, reason: "multiple devices on account", listed: true},
		{name: "model alone cannot disambiguate", body: twoDevices, input: Device{Model: "S50 Pro"}, reason: "multiple devices on account", listed: true},
		{name: "default matching model", body: oneDevice, input: Device{Model: "S50 Pro"}, want: Device{"serial-sentinel-secret-1234", "S50 Pro"}, listed: true},
		{name: "serial model lookup", body: twoDevices, input: Device{Serial: "other-serial-sentinel-5678"}, want: Device{"other-serial-sentinel-5678", "S30"}, listed: true},
		{name: "serial not found", body: oneDevice, input: Device{Serial: "absent-serial"}, reason: "--serial not found on account", listed: true},
		{name: "serial empty account", body: emptyDevices, input: Device{Serial: "absent-serial"}, reason: "incomplete catalog", listed: true},
		{name: "catalog matches supplied model", body: oneDevice, input: Device{"serial-sentinel-secret-1234", "S50 Pro"}, want: Device{"serial-sentinel-secret-1234", "S50 Pro"}, listed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: []string{tc.body}}
				c := selectionClient(t, f, &logs, level)
				got, err := c.SelectDevice(t.Context(), tc.input)
				if tc.reason == "" {
					if err != nil || got != tc.want {
						t.Fatalf("got %v error=%v", got, err)
					}
				} else {
					if !errors.Is(err, cloud.ErrConfig) || !strings.Contains(err.Error(), tc.reason) {
						t.Fatalf("wrong failure %v", err)
					}
					diagnostic.Log(c.logger, "selection failed", "selection", err)
					if !strings.Contains(logs.String(), tc.reason) {
						t.Fatalf("missing actionable error %s", &logs)
					}
					var option *diagnostic.OptionError
					wantOption, wantRule := "--serial", "select an existing device from the account catalog"
					if tc.reason == "incomplete catalog" {
						wantOption, wantRule = "--model", "incomplete catalog; name --model to select a received pair"
					}
					if !errors.As(err, &option) || option.Option != wantOption || option.Rule != wantRule {
						t.Fatalf("want fixed catalog option/rule, got %v", err)
					}
					if tc.reason == "multiple devices on account" {
						for _, s := range []string{"S50 Pro", "***1234", "S30", "***5678"} {
							if strings.Contains(logs.String(), s) || strings.Contains(err.Error(), s) {
								t.Fatalf("catalog label %q escaped ambiguous rejection", s)
							}
						}
					}
					assertSelectionSecrets(t, err.Error())
				}
				if tc.listed && !strings.Contains(strings.Join(f.paths, ","), "/v1/my-device") {
					t.Fatal("device list not requested")
				}
				if !tc.listed && len(f.paths) != 0 {
					t.Fatal("explicit selection performed discovery")
				}
				if tc.reason == "" && tc.listed {
					for _, s := range []string{`"level":"INFO"`, `"msg":"device_selected"`, tc.want.Model, "***"} {
						if !strings.Contains(logs.String(), s) {
							t.Fatalf("missing selection log %s", &logs)
						}
					}
				}
				assertSelectionSecrets(t, logs.String())
			}
		})
	}
}
func TestDeviceSelectionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"empty nullable catalog", `{"code":200,"msg":"token-sentinel-secret","data":{"items":null}}`, 200, cloud.ErrConfig},
		{"malformed", `{"code":200,"data":{"items":{}}}`, 200, cloud.ErrInvalid},
		{"unauthorized", oneDevice, 401, cloud.ErrReauthentication},
		{"rejected", oneDevice, 500, cloud.ErrRejected},
		{"transport", "", 0, cloud.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{tc.body}, status: tc.status}, &logs, slog.LevelDebug)
			_, err := c.SelectDevice(t.Context(), Device{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			assertSelectionSecrets(t, logs.String()+err.Error())
		})
	}
}
func TestDeviceSelectionPagination(t *testing.T) {
	var logs bytes.Buffer
	f := &selectionCloud{bodies: []string{
		strings.Replace(oneDevice, `"total":1`, `"total":2`, 1),
		`{"code":200,"msg":"ok","data":{"items":[{"deviceSn":"other-serial-sentinel-5678","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}],"current":2,"total":2}}`,
	}}
	c := selectionClient(t, f, &logs, slog.LevelInfo)
	got, err := c.SelectDevice(t.Context(), Device{Serial: "other-serial-sentinel-5678"})
	if err != nil || got.Model != "S30" {
		t.Fatalf("second page lookup failed: %v", err)
	}
	if len(f.queries) != 4 || f.queries[2] != "current=1&isContainShared=3&pageSize=100" || f.queries[3] != "current=2&isContainShared=3&pageSize=100" {
		t.Fatalf("queries=%v", f.queries)
	}
	assertSelectionSecrets(t, logs.String())
}
func TestDeviceSelectionMaskedShortSerialAndUntrustedModel(t *testing.T) {
	for _, serial := range []string{"x", "1234", "12345"} {
		var logs bytes.Buffer
		body := strings.Replace(oneDevice, "serial-sentinel-secret-1234", serial, 1)
		body = strings.Replace(body, "S50 Pro", "token-sentinel-secret", 1)
		c := selectionClient(t, &selectionCloud{bodies: []string{body}}, &logs, slog.LevelInfo)
		if _, err := c.SelectDevice(t.Context(), Device{}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs.String(), serial) {
			t.Fatal("full short serial in logs")
		}
		assertSelectionSecrets(t, logs.String())
	}
}
func TestDeviceSelectionCanceled(t *testing.T) {
	var logs bytes.Buffer
	c := selectionClient(t, &selectionCloud{}, &logs, slog.LevelDebug)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.SelectDevice(ctx, Device{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	assertSelectionSecrets(t, logs.String())
}

func TestDeviceSelectionTransportFailure(t *testing.T) {
	var logs bytes.Buffer
	// The fake transport returns a synthetic secret-bearing error on the list.
	c := selectionClient(t, &selectionCloud{}, &logs, slog.LevelDebug)
	_, err := c.SelectDevice(t.Context(), Device{})
	if !errors.Is(err, cloud.ErrTransport) {
		t.Fatalf("got %v, want transport failure", err)
	}
	assertSelectionSecrets(t, logs.String()+err.Error())
}

// The fake exposes its shared-only telescope only in connect-screen list mode.
// This models the contract; it is not an observation of server filtering.
func TestSharedOnlySelection(t *testing.T) {
	var logs bytes.Buffer
	f := &selectionCloud{bodies: []string{oneDevice}, sharedOnly: true}
	c := selectionClient(t, f, &logs, slog.LevelInfo)
	got, err := c.SelectDevice(t.Context(), Device{})
	if err != nil || got != (Device{"serial-sentinel-secret-1234", "S50 Pro"}) {
		t.Fatalf("shared-only selection=%v error=%v", got, err)
	}
}
