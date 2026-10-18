package cloud

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const loginFixture = `{"code":0,"msg":"token-sentinel","data":{"accessToken":"opaque-token-sentinel","accessTokenType":"ignored","accessExpire":-1,"refreshAfter":2147483647}}`

func TestAccountWire(t *testing.T) {
	c, err := EmailCredentials("reader@example.invalid", "Example-Only-123")
	if err != nil {
		t.Fatal(err)
	}
	for _, renewal := range []bool{false, true} {
		b := c.LoginBody(renewal)
		category := "1"
		if renewal {
			category = "0"
		}
		want := `{"authKey":"reader@example.invalid","authType":"email","category":` + category + `,"language":0,"password":"Example-Only-123"}`
		if string(b) != want {
			t.Fatal("login fixture mismatch")
		}
	}
	c, err = EmailCredentials("reader@example.invalid", "<> &'=😀123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c.LoginBody(false)), `\u003c\u003e \u0026\u0027\u003d😀123`) {
		t.Fatal("Gson escaping mismatch")
	}
	for _, p := range []string{"short", strings.Repeat("😀", 17)} {
		if _, err := EmailCredentials("a@b.invalid", p); !errors.Is(err, ErrConfig) {
			t.Fatal("password UTF-16 bound")
		}
	}
}

func TestAccountResponses(t *testing.T) {
	tok, err := ParseLogin(200, []byte(loginFixture), false)
	if err != nil || tok.Value() != "opaque-token-sentinel" {
		t.Fatal("token installation")
	}
	for _, b := range []string{
		`{"code":0,"msg":"sentinel","data":null}`,
		`{"code":0,"msg":"sentinel","data":{"accessToken":""}}`,
		`{"code":0,"msg":"sentinel","data":{"accessToken":"a\r\nb"}}`,
		loginFixture + ` {}`, strings.Repeat("x", MaxBody+1),
	} {
		if _, err := ParseLogin(200, []byte(b), false); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted invalid login")
		}
	}
	for _, status := range []int{201, 204, 400, 401, 403, 404, 423, 429, 500} {
		_, err := ParseLogin(status, []byte(`{"code":9876,"msg":"password-sentinel","data":null}`), false)
		var ae *AccountError
		if !errors.As(err, &ae) || ae.Status != status || !ae.HasCode || ae.Code != 9876 {
			t.Fatalf("status/code lost: %v", err)
		}
		if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "sentinel") {
			t.Fatal("error leaked secret")
		}
	}
	if _, err := ParseLogin(201, []byte(loginFixture), true); err != nil {
		t.Fatal(err)
	}
	if err := ParseProfile(200, []byte(`{"code":0,"msg":"ok","data":{"id":"private","email":"a@b.invalid"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := ParseProfile(200, []byte(`{"code":0,"msg":"ok","data":null}`)); err != nil {
		t.Fatal("unused profile data rejected")
	}
}
