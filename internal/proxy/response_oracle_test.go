package proxy

import "testing"

func TestResponseOracleRejectsWrongIdentity(t *testing.T) {
	for _, b := range []string{
		`{"jsonrpc":"2.0","id":70,"result":{"state":"idle"}}`,
		`{"jsonrpc":"2.0","id":700,"result":{"state":"idle"}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"state":"wrong"}}`,
		`{"jsonrpc":"2.0","id":7,"error":{"code":1}}`,
		`{"jsonrpc":"2.0","result":{"state":"idle"}}`,
	} {
		if checkResponse(b, 7) == nil {
			t.Errorf("accepted wrong reply %s", b)
		}
	}
	if err := checkResponse(`{"jsonrpc":"2.0","id":7,"result":{"state":"idle"}}`, 7); err != nil {
		t.Fatal(err)
	}
}
