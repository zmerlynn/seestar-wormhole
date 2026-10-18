package cloud

import (
	"errors"
	"testing"
)

func TestNullableUnusedTokenMetadata(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"msg":"ok","data":{"accessToken":"opaque","accessTokenType":null}}`,
		`{"code":0,"msg":null,"data":{"accessToken":"opaque"}}`,
		`{"code":0,"data":{"accessToken":"opaque","accessTokenType":null}}`,
		`{"code":0,"msg":null,"data":{"accessToken":"opaque","accessTokenType":null}}`,
		`{"code":0,"data":{"accessToken":"opaque"}}`,
	} {
		for _, renewal := range []bool{false, true} {
			status := 200
			if renewal {
				status = 201
			}
			tok, err := ParseLogin(status, []byte(body), renewal)
			if err != nil || tok.Value() != "opaque" {
				t.Errorf("renewal=%t body=%s: token=%v err=%v, want valid token", renewal, body, tok, err)
			}
		}
	}
	for _, body := range []string{
		`{"code":0,"msg":null,"data":{"id":"profile"}}`,
		`{"code":0,"data":{"id":"profile"}}`,
	} {
		if err := ParseProfile(200, []byte(body)); err != nil {
			t.Errorf("nullable/absent profile message: %v", err)
		}
	}
}

func TestNullableMetadataStillRequiresValidToken(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"msg":null,"data":{"accessToken":null}}`,
		`{"code":0,"data":{"accessTokenType":null}}`,
		`{"code":0,"data":{"accessToken":" "}}`,
		`{"code":0,"data":{"accessToken":"a\u007fb"}}`,
		`{"code":0,"data":{"accessToken":"opaque"}} {}`,
	} {
		if _, err := ParseLogin(200, []byte(body), false); !errors.Is(err, ErrInvalid) {
			t.Errorf("body=%s: err=%v, want invalid response", body, err)
		}
	}
}
