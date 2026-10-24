package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"encoding/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Literal rejected fixture includes secrets in values, numbers, unknown keys and
// header parameters. None may be copied to diagnostics, even on error paths.
const rejectedResponse = `{"code":9876,"msg":["message-secret"],"data":{"accessToken":87654321,"password":"password-secret","id":"profile-secret","items":[{"deviceSn":"serial-secret"}],"location":{"city":"city-secret","isShared":true,"id":null},"key-secret":"value-secret"},"unknown-secret":false}`

func TestRejectedResponseDiagnostics(t *testing.T) {
	for _, phase := range []string{"login", "renewal", "account_profile", "device_list", "device_detail", "allocation", "release"} {
		for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
			t.Run(phase+level.String(), func(t *testing.T) {
				var logs bytes.Buffer
				origin, _ := url.Parse("https://fixture.invalid")
				c := &Client{base: origin, logger: redactionTestLogger(&logs, level), timeout: time.Second, stopTimeout: time.Second, transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					status, body := 200, rejectedResponse
					if phase == "account_profile" || phase == "release" {
						// These phases consume only code; give it the wrong type.
						body = strings.Replace(body, `"code":9876`, `"code":"secret"`, 1)
					}
					if phase == "renewal" && r.URL.Path == "/v1/user/current-profile" {
						status = 401
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {`application/json; secret=header-secret`}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				var err error
				switch phase {
				case "login", "renewal", "account_profile":
					creds, _ := cloud.EmailCredentials("reader@example.invalid", "Example-Only-123")
					m := cloud.NewAccountMachine(creds)
					event := cloud.AccountEvent{Kind: cloud.AccountBegin}
					if phase != "login" {
						a := m.Step(event, 0)
						token, _ := cloud.ParseLogin(200, []byte(`{"code":0,"data":{"accessToken":"token-secret"}}`), false)
						a = m.Step(cloud.AccountEvent{Kind: cloud.AccountLoginDone, Sequence: a.Sequence, Token: token}, 0)
						m.Step(cloud.AccountEvent{Kind: cloud.AccountProfileDone, Sequence: a.Sequence}, 0)
						event.Kind = cloud.AccountCheckProfile
					}
					err = runAccount(t.Context(), c, m, event).err
				case "device_list":
					_, _, _, err = c.devices(t.Context())
				case "device_detail":
					_, err = c.Detail(t.Context(), Device{"serial", "S50 Pro"})
				case "allocation":
					_, err = c.Acquire(t.Context(), Device{"serial", "S50 Pro"})
				case "release":
					a, _, _ := cloud.ParseOnline([]byte(online))
					err = c.stopWithContext(t.Context(), a.ID())
				}
				if err == nil {
					t.Fatal("fixture was accepted")
				}
				log := logs.String()
				for _, secret := range []string{"9876", "message-secret", "87654321", "password-secret", "profile-secret", "serial-secret", "city-secret", "key-secret", "unknown-secret", "value-secret", "header-secret", "token-secret"} {
					if strings.Contains(log, secret) {
						t.Fatalf("secret %q escaped: %s", secret, log)
					}
				}
				if level == slog.LevelInfo {
					if strings.Contains(log, "cloud_response_rejected") || strings.Contains(log, "shape") || strings.Contains(log, "validation_rule") {
						t.Fatalf("rejection metadata at info: %s", log)
					}
					return
				}
				wantLength := len(rejectedResponse)
				if phase == "account_profile" || phase == "release" {
					wantLength += len(`"secret"`) - len(`9876`)
				}
				decoder := jsontext.NewDecoder(strings.NewReader(log))
				matched := false
				for decoder.PeekKind() != 0 {
					var event map[string]any
					if err := json.UnmarshalDecode(decoder, &event, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					if event["msg"] != "cloud_response_rejected" {
						continue
					}
					if event["level"] != "DEBUG" {
						t.Errorf("rejection event has level %v, want DEBUG", event["level"])
					}
					if event["phase"] == phase {
						matched = true
						if event["http_status"] != float64(200) || event["body_length"] != float64(wantLength) || event["cloud_code"] != nil || event["content_type"] != "application/json" {
							t.Errorf("incorrect response metadata: %v", event)
						}
					}
				}
				if !matched {
					t.Errorf("no rejection event for %s: %s", phase, log)
				}

				for _, want := range []string{`"msg":"cloud_response_rejected"`, `"phase":"` + phase + `"`, `"http_status":200`, `"content_type":"application/json"`, `"body_length":`, `"validation_field":`, `"validation_rule":`} {
					if !strings.Contains(log, want) {
						t.Errorf("missing %s in %s", want, log)
					}
				}
				if !errors.Is(err, cloud.ErrInvalid) {
					t.Fatalf("got %v, want invalid response", err)
				}
			})
		}
	}
}

func TestLoginValidationRuleDiagnostic(t *testing.T) {
	var logs bytes.Buffer
	origin, _ := url.Parse("https://fixture.invalid")
	c := &Client{base: origin, logger: diagnostic.Logger("text", &logs, slog.LevelDebug), transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"accessToken":87654321}}`))}, nil
	})}
	creds, _ := cloud.EmailCredentials("reader@example.invalid", "Example-Only-123")
	runAccount(t.Context(), c, cloud.NewAccountMachine(creds), cloud.AccountEvent{Kind: cloud.AccountBegin})
	for _, want := range []string{"validation_field=data.accessToken", "validation_rule=string"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q: %s", want, logs.String())
		}
	}
}

func TestRejectionDiagnosticAbsentOnSuccessOrNoResponse(t *testing.T) {
	var logs bytes.Buffer
	c := &Client{logger: diagnostic.Logger("json", &logs, slog.LevelDebug)}
	c.rejected(t.Context(), "login", responseInfo{status: 200}, []byte(rejectedResponse), nil)
	c.rejected(t.Context(), "login", responseInfo{}, nil, cloud.ErrTransport)
	if logs.Len() != 0 {
		t.Fatalf("unexpected rejection diagnostic: %s", logs.String())
	}
}

func FuzzRejectedResponseDiagnostics(f *testing.F) {
	for _, b := range []string{rejectedResponse, `{"code":0,"password":"sentinel"}`, `{"code":87654321}`, `{"code":200,"code":87654321}`, `{"sentinel":123}`, `["sentinel",{"data":"sentinel"}]`, `{"code":`, `null`} {
		f.Add([]byte(b))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var logs bytes.Buffer
		c := &Client{logger: redactionTestLogger(&logs, slog.LevelDebug)}
		c.rejected(t.Context(), "login", responseInfo{status: 200}, b, cloud.ErrInvalid)
		if bytes.Contains(logs.Bytes(), []byte("sentinel")) || bytes.Contains(logs.Bytes(), []byte("87654321")) {
			t.Fatal("diagnostic contains untrusted sentinel")
		}
		if logs.Len() > 1024 {
			t.Fatal("diagnostic exceeds output budget")
		}
	})
}
