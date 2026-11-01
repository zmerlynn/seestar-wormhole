package cloudhttp

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

func assertCloudSecretSafe(t *testing.T, err error, logs string) {
	t.Helper()
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Error("failure", "error", err)
	text := withoutIdentitySerialLogs(t, logs) + out.String() + fmt.Sprintf("%v %+v %#v", err, err, err)
	for _, secret := range []string{"sentinel", "SYNTHETICUID", "Example-Only", "reader@example.invalid"} {
		if strings.Contains(text, secret) {
			t.Fatal("secret or identity entered error/log output")
		}
	}
	// Inspect wrapped causes as well as the public error text.
	if e, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range e.Unwrap() {
			assertCloudSecretSafe(t, cause, "")
		}
	} else if cause := errors.Unwrap(err); cause != nil {
		assertCloudSecretSafe(t, cause, "")
	}
}

func TestCloudMetamorphicSafetyLiterals(t *testing.T) {
	for _, tc := range []struct{ name, family, body string }{
		{"missing_token", "login", `{"code":200,"data":{}}`},
		{"null_token", "login", `{"code":200,"data":{"accessToken":null}}`},
		{"mistyped_token", "login", `{"code":200,"msg":"synthetic-password-sentinel","data":{"accessToken":{"synthetic-token-sentinel":1}}}`},
		{"control_in_token", "login", `{"code":200,"data":{"accessToken":"synthetic-token-sentinel\r\n"}}`},
		{"mistyped_profile_code", "profile", `{"code":"synthetic-token-sentinel","data":{"id":"synthetic-profile-sentinel"}}`},
		{"missing_serial", "devices", `{"code":200,"data":{"items":[{"deviceModel":"S50 Pro"}]}}`},
		{"mistyped_model", "devices", `{"code":200,"data":{"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":{"synthetic-password-sentinel":1}}]}}`},
		{"missing_status", "detail", `{"code":200,"data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-wifi"}}`},
		{"null_status", "detail", `{"code":200,"data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":null,"ssid":"synthetic-wifi"}}`},
		{"mistyped_status", "detail", `{"code":200,"data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":{"synthetic-token-sentinel":1}}}`},
		{"mistyped_lan_name", "lan_detail", `{"code":200,"data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":1,"ssid":{"synthetic-ssid-sentinel":1}}}`},
		{"missing_allocation", "allocation", `{"code":200,"data":{}}`},
		{"mistyped_password", "allocation", `{"code":200,"data":{"conId":"synthetic-conid-sentinel","deviceUid":"SYNTHETICUID00000001","sdkLicenseKey":"synthetic-license-sentinel","username":"synthetic-user-sentinel","password":{"synthetic-password-sentinel":1}}}`},
		{"mistyped_stop_code", "stop", `{"code":"synthetic-token-sentinel","data":null}`},
		{"missing_realm", "license", `{"keyMask":"AAECAwQFBgcICQoLDA0ODw=="}`},
		{"mistyped_realm", "license", `{"realm":{"synthetic-token-sentinel":1}}`},
		{"bad_mask_extent", "license", `{"realm":"example","keyMask":"AAEC"}`},
		{"mistyped_override", "license", `{"realm":"example","standalone":{"all":{"synthetic-password-sentinel":1}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cloudProjection(tc.family, []byte(tc.body))
			if err == nil {
				t.Fatal("safety mutation accepted")
			}
			assertCloudSecretSafe(t, err, "")
		})
	}
}

func TestCloudMetamorphicSafetyRequiredFields(t *testing.T) {
	// These are the client's safety prerequisites for using the consumed
	// connection material/identity, not claims about optional DTO fields.
	fields := map[string][]string{
		"login": {"accessToken"}, "profile": {"id"},
		"devices": {"deviceSn", "deviceModel", "deviceStatus", "ssid"}, "detail": {"deviceSn", "deviceModel", "deviceStatus", "ssid"},
		"lan_detail": {"deviceSn", "deviceModel", "deviceStatus", "ssid"},
		"allocation": {"conId", "deviceUid", "sdkLicenseKey", "username", "password"},
	}
	for _, f := range metamorphicFixtures {
		for _, field := range fields[f.name] {
			for _, bad := range []string{"missing", `null`, `{}`, `[]`, `true`} {
				t.Run(f.name+"/"+field+"/"+bad, func(t *testing.T) {
					root := metamorphicObject(t, []byte(f.body))
					data := metamorphicObject(t, root["data"])
					obj := data
					if f.name == "devices" {
						var items []jsontext.Value
						if err := json.Unmarshal(data["items"], &items, json.MatchCaseInsensitiveNames(true)); err != nil {
							t.Fatal(err)
						}
						obj = metamorphicObject(t, items[0])
					}
					if bad == "missing" {
						delete(obj, field)
					} else {
						obj[field] = jsontext.Value(bad)
					}
					if f.name == "devices" {
						data["items"] = metamorphicJSON(t, []any{obj})
					}
					root["data"] = metamorphicJSON(t, data)
					_, err := cloudProjection(f.name, metamorphicJSON(t, root))
					if err == nil {
						t.Fatal("missing or mistyped safety prerequisite accepted")
					}
					assertCloudSecretSafe(t, err, "")
				})
			}
		}
	}
}

func TestCloudMetamorphicSafetyBounds(t *testing.T) {
	for _, f := range metamorphicFixtures {
		t.Run(f.name, func(t *testing.T) {
			limit := cloud.MaxBody
			if f.name == "license" {
				limit = 16384
			}
			for _, extra := range []int{0, 1} {
				b := []byte(f.body + strings.Repeat(" ", limit-len(f.body)+extra))
				_, err := cloudProjection(f.name, b)
				if (err != nil) != (extra == 1) {
					t.Fatalf("body boundary extra=%d: %v", extra, err)
				}
				assertCloudSecretSafe(t, err, "")
			}
		})
	}
	for _, count := range []int{256, 257} {
		items := make([]map[string]any, count)
		for i := range items {
			items[i] = map[string]any{"deviceSn": fmt.Sprintf("synthetic-serial-sentinel-%03d", i), "deviceModel": "S50 Pro", "ssid": "synthetic-wifi", "deviceStatus": 1}
		}
		b := metamorphicJSON(t, map[string]any{"code": 200, "data": map[string]any{"items": items, "total": count}})
		_, _, err := cloud.ParseDevices(b)
		if (err != nil) != (count == 257) {
			t.Fatalf("catalog boundary count=%d: %v", count, err)
		}
		assertCloudSecretSafe(t, err, "")
	}
	for _, n := range []int{4096, 4097} {
		b := metamorphicJSON(t, map[string]any{"data": map[string]any{"accessToken": strings.Repeat("x", n)}})
		_, err := cloud.ParseLogin(200, b, false)
		if (err != nil) != (n == 4097) {
			t.Fatalf("token boundary size=%d: %v", n, err)
		}
	}
}

func TestCloudMetamorphicSafetyRefusals(t *testing.T) {
	for _, code := range []int{300403, 300404, 300405, 300406} {
		for _, f := range metamorphicFixtures[2:7] {
			root := metamorphicObject(t, []byte(f.body))
			root["code"] = metamorphicJSON(t, code)
			root["msg"] = jsontext.Value(`"synthetic-password-sentinel"`)
			_, err := cloudProjection(f.name, metamorphicJSON(t, root))
			want := cloud.ErrBusy
			if code == 300405 {
				want = cloud.ErrRejected
			} else if code == 300406 {
				want = cloud.ErrOffline
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s refusal code=%d: %v", f.name, code, err)
			}
			assertCloudSecretSafe(t, err, "")
		}
	}
	for _, status := range []int{400, 401, 403, 500} {
		_, err := cloud.ParseLogin(status, []byte(metamorphicFixtures[0].body), false)
		if err == nil {
			t.Fatal("HTTP auth refusal accepted with valid token")
		}
		assertCloudSecretSafe(t, err, "")
	}
}

func TestCloudMetamorphicSelectionSafety(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		selectBySerial bool
		want           error
	}{
		{"extra_unrelated", `{"code":200,"data":{"total":2,"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"synthetic-other-sentinel","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`, true, nil},
		{"ambiguous_implicit", `{"code":200,"data":{"total":2,"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"synthetic-other-sentinel","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`, false, cloud.ErrConfig},
		{"conflicting_matches", `{"code":200,"data":{"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`, true, cloud.ErrConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c, err := newAuthenticatedFixture(t, Config{
				Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					body := tc.body
					if r.URL.Query().Get("current") != "1" {
						return nil, errors.New("unexpected catalog page")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			d := Device{}
			if tc.selectBySerial {
				d.Serial = "synthetic-serial-sentinel"
			}
			got, err := c.SelectDevice(t.Context(), d)
			if !errors.Is(err, tc.want) {
				t.Fatalf("selection class: %v", err)
			}
			if err == nil && got != (Device{Serial: "synthetic-serial-sentinel", Model: "S50 Pro"}) {
				t.Fatal("extra entry changed selected identity")
			}
			assertCloudSecretSafe(t, err, logs.String())
		})
	}
}

func TestCloudMetamorphicLicenseTail(t *testing.T) {
	// A literal document-order prefix is necessary: encoding a map would sort
	// regions across the first-ten boundary and change which entries are used.
	const prefix = `{"realm":"example","standalone":{"a":"a.example.invalid","b":"b.example.invalid","c":"c.example.invalid","d":"d.example.invalid","e":"e.example.invalid","f":"f.example.invalid","g":"g.example.invalid","h":"h.example.invalid","i":"i.example.invalid","j":"j.example.invalid"`
	want, err := iotc.ParseLicenseJSON([]byte(prefix + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{`,"unused":null`, `,"unused":{"synthetic-password-sentinel":true}`, `,"unused":[1,2,3]`} {
		got, err := iotc.ParseLicenseJSON([]byte(prefix + tail + `}}`))
		if err != nil || got.Realm != want.Realm || got.Mask != want.Mask || len(got.Standalone) != 10 || got.StandaloneOmitted != 1 {
			t.Fatal("unexamined license tail changed discovery settings")
		}
		for region, host := range want.Standalone {
			if got.Standalone[region] != host {
				t.Fatal("unexamined tail replaced override")
			}
		}
		assertCloudSecretSafe(t, err, "")
	}
}
