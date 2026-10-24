package cloudhttp

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPRejectionShape(t *testing.T) {
	for _, failure := range []string{"http", "authorization", "read"} {
		for _, phase := range []string{"release", "device_list", "device_detail", "allocation", "login", "account_profile"} {
			t.Run(failure+"/"+phase, func(t *testing.T) {
				var sink bytes.Buffer
				origin, _ := url.Parse("https://fixture.invalid")
				c := &Client{base: origin, timeout: time.Second, stopTimeout: time.Second, logger: redactionTestLogger(&sink, slog.LevelDebug)}
				c.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					body := `{"code":9876}`
					if phase == "device_list" {
						body = `{"code":9876,"data":{"items":[],"total":0}}`
					}
					if phase == "device_detail" {
						body = `{"code":9876,"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
					}
					if phase == "allocation" {
						body = online
					}
					if phase == "login" {
						body = `{"code":200,"data":{"accessToken":"token-secret"}}`
					}
					status := 500
					var reader io.Reader = strings.NewReader(body)
					if failure == "authorization" {
						status = 403
					}
					if failure == "read" {
						status = 200
						reader = io.MultiReader(reader, failReader{})
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(reader)}, nil
				})
				var err error
				switch phase {
				case "login", "account_profile":
					creds, _ := cloud.EmailCredentials("reader@example.invalid", "Example-Only-123")
					m := cloud.NewAccountMachine(creds)
					event := cloud.AccountEvent{Kind: cloud.AccountBegin}
					if phase == "account_profile" {
						a := m.Step(event, 0)
						token, _ := cloud.ParseLogin(200, []byte(`{"code":200,"data":{"accessToken":"token-secret"}}`), false)
						a = m.Step(cloud.AccountEvent{Kind: cloud.AccountLoginDone, Sequence: a.Sequence, Token: token}, 0)
						m.Step(cloud.AccountEvent{Kind: cloud.AccountProfileDone, Sequence: a.Sequence}, 0)
						event.Kind = cloud.AccountCheckProfile
					}
					err = runAccount(t.Context(), c, m, event).err
				case "release":
					a, _, _ := cloud.ParseOnline([]byte(online))
					err = c.stopWithContext(t.Context(), a.ID())
				case "device_list":
					_, _, _, err = c.devices(t.Context())
				case "device_detail":
					_, err = c.Detail(t.Context(), Device{"serial", "S50 Pro"})
				case "allocation":
					_, err = c.Acquire(t.Context(), Device{"serial", "S50 Pro"})
				}
				want := cloud.ErrRejected
				if failure == "authorization" {
					want = cloud.ErrUnauthorized
				}
				if failure == "read" {
					want = cloud.ErrTransport
				}
				if phase == "login" || phase == "account_profile" {
					if failure != "read" {
						want = cloud.ErrRejected
					}
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want HTTP rejection", err)
				}
				for _, secret := range []string{"9876", "token-secret", "read-secret"} {
					if strings.Contains(sink.String(), secret) {
						t.Fatalf("response escaped: %s", sink.String())
					}
				}
				found := false
				d := jsontext.NewDecoder(&sink)
				for d.PeekKind() != 0 {
					var event map[string]any
					if e := json.UnmarshalDecode(d, &event, json.MatchCaseInsensitiveNames(true)); e != nil {
						t.Fatal(e)
					}
					if event["msg"] == "cloud_response_rejected" && event["phase"] == phase {
						found = true
					}
				}
				if !found {
					t.Errorf("HTTP 500 %s rejected with no cloud_response_rejected event", phase)
				}
			})
		}
	}

}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("read-secret") }
