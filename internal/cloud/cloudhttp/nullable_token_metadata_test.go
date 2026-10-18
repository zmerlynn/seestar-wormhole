package cloudhttp

import (
	"net/http"
	"strings"
	"testing"
)

// Literal null/omission fixtures exercise token installation and the profile
// replay through the actual owner, independently of the pure parser tests.
func TestNullableMetadataLoginAndRenewal(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
	logins, profiles := 0, 0
	c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "login"):
			logins++
			if logins == 1 {
				return accountResponse(200, `{"code":0,"msg":null,"data":{"accessToken":"first","accessTokenType":null}}`), nil
			}
			return accountResponse(201, `{"code":0,"data":{"accessToken":"renewed"}}`), nil
		case strings.HasSuffix(r.URL.Path, "current-profile"):
			profiles++
			if profiles == 2 {
				return accountResponse(401, `{"code":1,"msg":null,"data":null}`), nil
			}
			want := "first"
			if profiles == 3 {
				want = "renewed"
			}
			if r.Header.Get("Authorization") != want || r.Header.Get("X-Astroimg-Authorization") != want {
				t.Error("profile did not use installed token")
			}
			return accountResponse(200, `{"code":0,"data":{"id":"profile"}}`), nil
		default:
			if r.Header.Get("Authorization") != "renewed" {
				t.Error("remote did not use renewed token")
			}
			return accountResponse(200, detail), nil
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = c.CheckProfile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Detail(t.Context(), Device{"serial", "S50 Pro"}); err != nil {
		t.Fatal(err)
	}
	if logins != 2 || profiles != 3 {
		t.Fatalf("login/profile calls=%d/%d, want 2/3", logins, profiles)
	}
}
