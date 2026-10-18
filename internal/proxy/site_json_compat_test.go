package proxy

import (
	"bytes"
	jsonv1 "encoding/json"
	"testing"
)

// V1 is used only as a byte comparator for the established rewrite contract.
func TestSiteJSONV1Bytes(t *testing.T) {
	line := []byte(`{"method":"set_setting","id":9007199254740993,"params":{"lat":0,"z":"<>&\u2028\u2029","a":1.2300e+04,"esc":"\u0061"},"future":{"z":-0,"a":1e+09}}` + "\r\n")
	var old map[string]jsonv1.RawMessage
	if err := jsonv1.Unmarshal(line, &old); err != nil {
		t.Fatal(err)
	}
	var params map[string]jsonv1.RawMessage
	if err := jsonv1.Unmarshal(old["params"], &params); err != nil {
		t.Fatal(err)
	}
	delete(params, "lat")
	old["params"], _ = jsonv1.Marshal(params)
	want, err := jsonv1.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	got, event := protectSite(line)
	if event == nil || !bytes.Equal(got, append(want, '\r', '\n')) {
		t.Fatalf("rewrite=%s; v1=%s", got, want)
	}
}
