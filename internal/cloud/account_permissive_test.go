package cloud

import "testing"

func TestAccountConsumesOnlyTokenAndCode(t *testing.T) {
	for _, value := range []string{`null`, `false`, `[]`, `{}`, `""`, `2147483648`, `1.5`} {
		body := `{"code":200,"msg":` + value + `,"data":{"accessToken":"opaque","accessTokenType":` + value + `,"accessExpire":` + value + `,"refreshAfter":` + value + `}}`
		for _, renewal := range []bool{false, true} {
			tok, err := ParseLogin(200, []byte(body), renewal)
			if err != nil || tok.Value() != "opaque" {
				t.Errorf("metadata=%s renewal=%t: token=%v err=%v", value, renewal, tok, err)
			}
		}
		if err := ParseProfile(200, []byte(`{"code":200,"msg":`+value+`,"data":{"id":"profile","country":`+value+`,"whitelistFunctions":`+value+`}}`)); err != nil {
			t.Errorf("unused profile=%s: %v", value, err)
		}
	}
	if err := ParseProfile(200, []byte(`{"code":200}`)); err != nil {
		t.Errorf("absent unused profile: %v", err)
	}
}
