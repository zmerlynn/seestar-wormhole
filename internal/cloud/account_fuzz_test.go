package cloud

import (
	"fmt"
	"strings"
	"testing"
)

func FuzzAccountResponses(f *testing.F) {
	for _, b := range []string{
		`{"data":{"accessToken":"opaque"}}`,
		`{"code":null,"data":{"accessToken":"opaque"}}`,
		`{}`,
		``,
		`{"code":0,"msg":null,"data":{"accessToken":"opaque","accessTokenType":null}}`,
		`{"code":0,"data":{"accessToken":"opaque"}}`,
		`{"code":0,"msg":null,"data":{"id":"profile"}}`,
		`{"code":0,"data":{"id":"profile"}}`,
		loginFixture,
		observedLoginFixture,
		`{"code":200}`,
		`{"code":200,"future":{"accessToken":false},"data":{"id":"profile","extra":[{"id":false}]}}`,
		`{"code":123,"msg":"password-sentinel","data":null}`,
		`{"code":0,"msg":"ok","data":{"id":"profile-sentinel"}}`,
	} {
		f.Add(uint16(200), []byte(b), false)
		f.Add(uint16(201), []byte(b), true)
	}
	f.Add(uint16(204), []byte{}, false)
	f.Fuzz(func(t *testing.T, status uint16, b []byte, renewal bool) {
		tok, err := ParseLogin(int(status), b, renewal)
		if err == nil && (tok.Value() == "" || len(b) > MaxBody) {
			t.Fatal("invalid successful token")
		}
		for _, e := range []error{err, ParseProfile(int(status), b)} {
			if e != nil && strings.Contains(fmt.Sprintf("%v %+v %#v", e, e, e), "sentinel") {
				t.Fatal("parser leaked secret")
			}
		}
	})
}
