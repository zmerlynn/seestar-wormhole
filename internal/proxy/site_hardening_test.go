package proxy

import (
	"bytes"
	"fmt"
	"github.com/go-json-experiment/json"
	"log/slog"
	"testing"
)

func TestSiteControlMethodRejected(t *testing.T) {
	for _, method := range []string{"pi_set_time", "set_user_location", "set_setting", "get_device_state"} {
		// C0, DEL, and C1 control characters, including escaped NUL. Test
		// all positions so rejection does not depend on a known setter prefix.
		for _, control := range append([]rune{0, 1, '\t', '\n', '\r', 31, 127}, 128, 159) {
			for _, value := range []string{string(control) + method, method + string(control) + "suffix", method + string(control)} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				t.Run(fmt.Sprintf("%s/%U/%q", method, control, value), func(t *testing.T) {
					checkSiteInvalidFrame(t, `{"id":9,"method":`+string(raw)+`,"params":{}}`, -32600, "9")
				})
			}
		}
	}
}

func checkSiteInvalidFrame(t *testing.T, raw string, code int, id string) {
	t.Helper()
	line := []byte(raw + "\r\n")
	var output, reply, logs bytes.Buffer
	if err := copySite(&output, &siteChunkReader{line, 1}, slog.New(slog.NewJSONHandler(&logs, nil)), &reply); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"request rejected by site protection"}}`+"\r\n", id, code)
	if output.Len() != 0 || reply.String() != want {
		t.Fatalf("upstream=%s reply=%s want=%s", output.Bytes(), reply.Bytes(), want)
	}
	var event struct{ Msg, Level, Method string }
	if json.Unmarshal(logs.Bytes(), &event, json.MatchCaseInsensitiveNames(true)) != nil || event.Msg != "site_reject" || event.Level != "WARN" || event.Method != "" {
		t.Fatalf("unexpected rejection log: %s", logs.Bytes())
	}
}

func TestSiteUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"id":9,"method":"get_device_state","Method":"pi_set_time","extra":[{"x":1},{"x":2}],"n":1e9999}`,
		`{"id":9,"method":"get_device_state","params":{"text":"pi_set_time\u0000suffix"}}`,
		`{"id":9,"method":"unknown_é","params":{"text":"{\"x\":1,\"x\":2}"}}`,
	} {
		line := []byte(raw + "\r\n")
		if got, event := protectSite(line); !bytes.Equal(got, line) || event != nil {
			t.Fatalf("safe unknown fields changed: got=%s event=%+v", got, event)
		}
	}
}
