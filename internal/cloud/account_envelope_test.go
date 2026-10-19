package cloud

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Independent literals from the account DTO consumption contract: HTTP status
// and accessToken authorize installation; code is optional diagnostic metadata.
func TestAccountOptionalCodeLiterals(t *testing.T) {
	for _, body := range []string{
		`{"data":{"accessToken":"opaque"}}`,
		`{"code":null,"data":{"accessToken":"opaque"}}`,
	} {
		for _, renewal := range []bool{false, true} {
			tok, err := ParseLogin(200, []byte(body), renewal)
			if err != nil || tok.Value() != "opaque" {
				t.Errorf("renewal=%t body=%s: got token=%v err=%v, want opaque", renewal, body, tok, err)
			}
		}
	}
	for _, body := range []string{`{}`, `{"code":null}`, `{"data":null}`} {
		if err := ParseProfile(200, []byte(body)); err != nil {
			t.Errorf("profile body=%s: got %v, want success", body, err)
		}
	}
	for _, body := range []string{`{}`, `{"code":null}`} {
		_, err := ParseLogin(403, []byte(body), false)
		var ae *AccountError
		if !errors.As(err, &ae) || ae.HasCode || !errors.Is(err, ErrRejected) {
			t.Errorf("body=%s: got %v, want rejection without diagnostic code", body, err)
		}
	}
}

func TestAccountProfileCompletionLiterals(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
	}{
		{201, `{"code":0,"data":null}`},
		{201, `{}`},
		{201, `{"code":null}`},
		{204, ``},
	} {
		t.Run(fmt.Sprint(tt.status, "/", tt.body), func(t *testing.T) {
			if err := ParseProfile(tt.status, []byte(tt.body)); err != nil {
				t.Fatalf("got %v, want completed profile", err)
			}
		})
	}
}

func TestAccountEnvelopeSafetyLiterals(t *testing.T) {
	for _, body := range []string{
		`{"code":"secret","data":{"accessToken":"opaque"}}`,
		`{"code":false,"data":{"accessToken":"opaque"}}`,
		`{"code":{},"data":{"accessToken":"opaque"}}`,
		`{"code":[],"data":{"accessToken":"opaque"}}`,
		`{"code":1.5,"data":{"accessToken":"opaque"}}`,
		`{"code":9223372036854775808,"data":{"accessToken":"opaque"}}`,
		`{"data":{"accessToken":"opaque"}} {}`,
		`{"data":`,
		`{"data":{"accessToken":"opaque"}}` + strings.Repeat(" ", MaxBody),
	} {
		for _, renewal := range []bool{false, true} {
			if _, err := ParseLogin(200, []byte(body), renewal); !errors.Is(err, ErrInvalid) {
				t.Errorf("renewal=%t: got %v, want invalid login", renewal, err)
			}
		}
		for _, status := range []int{200, 201, 204} {
			if err := ParseProfile(status, []byte(body)); !errors.Is(err, ErrInvalid) {
				t.Errorf("status=%d: got %v, want invalid profile", status, err)
			}
		}
	}
	for _, body := range []string{
		`{}`, `{"data":null}`, `{"data":{}}`,
		`{"data":{"accessToken":null}}`,
		`{"data":{"accessToken":123}}`,
		`{"data":{"accessToken":true}}`,
		`{"data":{"accessToken":[]}}`,
		`{"data":{"accessToken":{}}}`,
		`{"data":{"accessToken":"  "}}`,
		`{"data":{"accessToken":"a\r\nb"}}`,
		`{"data":{"accessToken":"a\u007fb"}}`,
		`{"data":{"accessToken":"` + strings.Repeat("x", 4097) + `"}}`,
	} {
		for _, renewal := range []bool{false, true} {
			if _, err := ParseLogin(200, []byte(body), renewal); !errors.Is(err, ErrInvalid) {
				t.Errorf("renewal=%t: got %v, want invalid token", renewal, err)
			}
		}
	}
	for _, status := range []int{201, 204, 400, 401, 403, 500} {
		if _, err := ParseLogin(status, []byte(`{"data":{"accessToken":"opaque"}}`), false); !errors.Is(err, ErrRejected) {
			t.Errorf("interactive login status=%d: got %v, want rejection", status, err)
		}
	}
	for _, body := range []string{`{}`, `{"code":null}`, `{"code":"secret"}`, ``} {
		for _, status := range []int{401, 403, 500} {
			want := ErrRejected
			if status == 401 {
				want = ErrUnauthorized
			}
			if err := ParseProfile(status, []byte(body)); !errors.Is(err, want) {
				t.Errorf("status=%d: got %v, want %v", status, err, want)
			}
		}
	}
	for _, status := range []int{200, 201} {
		if err := ParseProfile(status, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("empty profile status=%d: got %v, want invalid", status, err)
		}
	}
}

func TestRemoteOptionalOutcomeCode(t *testing.T) {
	for _, body := range []string{
		`{"data":{"conId":"cleanup","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`,
		`{"code":null,"data":{"conId":"cleanup","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`,
	} {
		allocation, ids, err := ParseOnline([]byte(body))
		if err != nil || allocation.ConnectionID() != "cleanup" || len(ids) != 1 || ids[0].Value() != "cleanup" {
			t.Errorf("got allocation=%v ids=%v err=%v, want allocation with cleanup ownership", allocation, ids, err)
		}
		if err := ParseStop([]byte(body)); err != nil {
			t.Errorf("got %v, want successful stop", err)
		}
	}
}
