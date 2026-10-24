package proxy

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSiteFrameRefusalIDsAndWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, frame, id, rule string
		code                  int
	}{
		{"bare LF string", `{"id":"private-id","method":"foo"}` + "\n", `"private-id"`, "incomplete_frame", -32600},
		{"CR only number", `{"id":9007199254740993,"method":"foo"}` + "\r", `9007199254740993`, "incomplete_frame", -32600},
		{"EOF exponent", `{"id":-12.50e+2,"method":"foo"}`, `-12.50e+2`, "incomplete_frame", -32600},
		{"malformed suffix", `{"id":"private-id","method":` + "\r\n", `"private-id"`, "invalid_request", -32700},
		{"duplicate method before ID", `{"method":"foo","method":"bar","id":1e400}` + "\r\n", `1e400`, "invalid_request", -32700},
		{"nested duplicates before ID", `{"params":{"x":1,"x":2},"id":"private-id","method":"foo"}` + "\r\n", `"private-id"`, "invalid_request", -32700},
		{"escaped ID", `{"i\u0064":"\u0070rivate-id","method":` + "\r\n", `"\u0070rivate-id"`, "invalid_request", -32700},
		{"duplicate ID", `{"id":1,"id":2}` + "\r\n", `null`, "invalid_request", -32700},
		{"incomplete duplicate ID", `{"id":1,"id":` + "\r\n", `null`, "invalid_request", -32700},
		{"escaped duplicate ID", `{"id":1,"i\u0064":2}` + "\r\n", `null`, "invalid_request", -32700},
		{"single request batch", `[{"id":1}]` + "\r\n", `1`, "invalid_request", -32700},
		{"multiple request batch", `[{"id":1},{"id":2}]` + "\r\n", `null`, "invalid_request", -32700},
		{"nonobject", `true` + "\r\n", `null`, "invalid_request", -32700},
		{"null", `null` + "\r\n", `null`, "invalid_request", -32700},
		{"object ID", `{"id":{"id":1},"method":` + "\r\n", `null`, "invalid_request", -32700},
		{"array ID", `{"id":[1],"method":` + "\r\n", `null`, "invalid_request", -32700},
		{"boolean ID", `{"id":true,"method":` + "\r\n", `null`, "invalid_request", -32700},
		{"null ID", `{"id":null,"method":` + "\r\n", `null`, "invalid_request", -32700},
		{"missing ID", `{"method":` + "\r\n", `null`, "invalid_request", -32700},
		{"nested ID", `{"params":{"id":1},"method":` + "\r\n", `null`, "invalid_request", -32700},
		{"incomplete ID", `{"id":"private-id` + "\r\n", `null`, "invalid_request", -32700},
		{"unreachable ID", `{"params":oops,"id":1}` + "\r\n", `null`, "invalid_request", -32700},
		{"overlong prefix ID", `{"id":"private-id","params":"` + strings.Repeat("x", MaxSiteLine) + `"}` + "\r\n", `"private-id"`, "frame_too_large", -32600},
		{"overlong numeric ID", `{"id":` + strings.Repeat("1", MaxSiteLine) + `}` + "\r\n", `null`, "frame_too_large", -32600},
		{"overlong string ID", `{"id":"` + strings.Repeat("x", MaxSiteLine) + `"}` + "\r\n", `null`, "frame_too_large", -32600},
		{"overlong suffix ID", `{"params":"` + strings.Repeat("x", MaxSiteLine) + `","id":1}` + "\r\n", `null`, "frame_too_large", -32600},
		{"overlong at limit", `{"id":7}` + strings.Repeat(" ", MaxSiteLine-len(`{"id":7}`)), `7`, "frame_too_large", -32600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunk := range []int{1, 7, MaxSiteLine + 1} {
				var out, reply, logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				// A following ordinary frame proves oversized suffixes are drained and
				// recovery never changes the next request.
				const next = "{\"id\":99,\"method\":\"get_setting\"}\r\n"
				input := tc.frame
				wantOut := ""
				if strings.HasSuffix(input, "\n") {
					input += next
					wantOut = next
				}
				if err := copySite(&out, &siteChunkReader{[]byte(input), chunk}, logger, &reply); err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"request rejected by site protection"}}`+"\r\n", tc.id, tc.code)
				if out.String() != wantOut || reply.String() != want {
					t.Fatalf("chunk=%d forwarded=%q reply=%q want=%q", chunk, out.String(), reply.String(), want)
				}
				var record struct{ Msg, Level, Reason string }
				if bytes.Count(logs.Bytes(), []byte("\n")) != 1 || json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)) != nil || record.Msg != "site_reject" || record.Level != "WARN" || record.Reason != tc.rule {
					t.Fatalf("log=%s want WARN rule=%s", logs.Bytes(), tc.rule)
				}
				if strings.Contains(logs.String(), "private-id") {
					t.Fatalf("ID leaked: %s", logs.Bytes())
				}
			}
		})
	}
}
