package proxy

import (
	"bytes"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
)

func TestSiteProtectedSettersTCP(t *testing.T) {
	for _, port := range []uint16{4400, 4700, 4701, 4702} {
		for _, method := range []string{"set_user_location", "pi_set_time", "scope_set_location", "scope_set_time"} {
			for _, id := range []string{`9007199254740993`, `"client-id"`} {
				for _, params := range []string{`{"lat":35,"time_zone":"private-zone"}`, `[35,-105]`} {
					t.Run(fmt.Sprintf("%d/%s/%s/%s", port, method, id, params), func(t *testing.T) {
						client, remote := siteTransportPair(t, port)
						request := fmt.Sprintf(`{"id":%s,"method":%q,"params":%s}`+"\r\n", id, method, params)
						if _, err := io.WriteString(client, request); err != nil {
							t.Fatal(err)
						}
						if err := client.CloseWrite(); err != nil {
							t.Fatal(err)
						}
						got, err := io.ReadAll(remote)
						if err != nil || len(got) != 0 {
							t.Fatalf("setter reached device: %s err=%v", got, err)
						}
						remote.(*net.TCPConn).CloseWrite()
						got, err = io.ReadAll(client)
						want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"site_protected"}}`+"\r\n", id)
						if err != nil || string(got) != want {
							t.Fatalf("reply=%s err=%v want=%s", got, err, want)
						}
					})
				}
			}
		}
	}
}

func TestSiteProtectedSettingTCP(t *testing.T) {
	for _, tc := range []struct{ name, params, want string }{
		{"mixed", `{"lat":35,"lon":-105,"time_zone":"private-zone","year":2001,"mon":1,"day":2,"hour":3,"min":4,"sec":5,"gain":80}`, `{"id":7,"method":"set_setting","params":{"gain":80}}` + "\r\n"},
		{"only", `{"lat":35,"lon":-105,"time_zone":"private-zone"}`, ""},
		{"clock only", `{"year":2001,"mon":1,"day":2,"hour":3,"min":4,"sec":5}`, ""},
		{"unchanged", `{ "gain" : 80, "unknown":9007199254740993 }`, `{ "id":7, "method":"set_setting", "params":{ "gain" : 80, "unknown":9007199254740993 } }` + "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, remote := siteTransportPair(t, 4700)
			request := `{ "id":7, "method":"set_setting", "params":` + tc.params + " }\r\n"
			io.WriteString(client, request)
			client.CloseWrite()
			got, err := io.ReadAll(remote)
			if err != nil || string(got) != tc.want {
				t.Fatalf("upstream=%s err=%v want=%s", got, err, tc.want)
			}
			remote.(*net.TCPConn).CloseWrite()
			got, err = io.ReadAll(client)
			want := ""
			if tc.want == "" {
				want = `{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"site_protected"}}` + "\r\n"
			}
			if err != nil || !bytes.Equal(got, []byte(want)) {
				t.Fatalf("reply=%s err=%v want=%s", got, err, want)
			}
		})
	}
}

func TestSiteProtectedLogsAndIDs(t *testing.T) {
	for _, tc := range []struct{ request, method, fields, id string }{
		{`{"id":115,"method":"set_user_location","params":{"lat":12.345,"lon":67.891,"private-key":"private-value"}}`, "set_user_location", "lat,lon", "115"},
		{`{"id":"private-id","method":"pi_set_time","params":[{"time_zone":"private-zone","year":2001}]}`, "pi_set_time", "time_zone,year", `"unavailable"`},
		{`{"id":1e400,"method":"pi_set_time","params":{}}`, "pi_set_time", "", `"unavailable"`},
		{`{"id":9223372036854775808,"method":"set_user_location","params":{}}`, "set_user_location", "", `"unavailable"`},
		{`{"id":5,"method":"scope_set_time","params":["private-clock"]}`, "scope_set_time", "", "5"},
		{`{"id":6,"method":"scope_set_location","params":{"latitude":12.345}}`, "scope_set_location", "", "6"},
		{`{"id":7,"method":"set_setting","params":{"lat":12.345,"hour":2001,"gain":80}}`, "set_setting", "lat,hour", "7"},
		{`{"id":8,"method":"set_setting","params":{"lon":67.891}}`, "set_setting", "lon", "8"},
	} {
		t.Run(tc.method+tc.id, func(t *testing.T) {
			var out, reply, logs bytes.Buffer
			err := copySite(&out, strings.NewReader(tc.request+"\r\n"), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				// Timestamps can coincidentally contain the synthetic coordinate values.
				if a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			}})), &reply)
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Msg, Method, Reason string
				ID                  jsontext.Value
				Fields              []string
			}
			if bytes.Count(logs.Bytes(), []byte("\n")) != 1 || json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)) != nil || record.Msg != "site_protected" || record.Reason != "site_protected" || record.Method != tc.method || string(record.ID) != tc.id || strings.Join(record.Fields, ",") != tc.fields {
				t.Fatalf("log=%s want method=%s id=%s fields=%s", logs.Bytes(), tc.method, tc.id, tc.fields)
			}
			for _, secret := range []string{"12.345", "67.891", "2001", "private-"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("log contains values: %s", logs.Bytes())
				}
			}
		})
	}
}

func TestSiteProtectedIDAndParamShapes(t *testing.T) {
	for _, method := range []string{"set_user_location", "pi_set_time", "scope_set_location", "scope_set_time"} {
		for _, tc := range []struct{ id, replyID string }{
			{`null`, `null`},
			{`true`, `null`},
			{`{}`, `null`},
			{`[]`, `null`},
			{`"string-id"`, `"string-id"`},
			{`9007199254740993`, `9007199254740993`},
			{`9223372036854775808`, `9223372036854775808`},
			{`1e400`, `1e400`},
			{`-12.50e+2`, `-12.50e+2`},
		} {
			for _, params := range []string{"", `,"params":null`, `,"params":[]`, `,"params":42`, `,"params":{}`, `,"params":[null]`} {
				line := []byte(`{"id":` + tc.id + `,"method":"` + method + `"` + params + "}\r\n")
				out, event := protectSite(line)
				want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"site_protected"}}`+"\r\n", tc.replyID)
				if len(out) != 0 || event == nil || string(event.rejection) != want || event.reason != "site_protected" {
					t.Fatalf("request=%s output=%s event=%+v want=%s", line, out, event, want)
				}
			}
		}
	}
}

func TestSiteSettingPreservesUnknownFields(t *testing.T) {
	input := []byte(`{"id":9007199254740993,"ID":99,"method":"set_setting","Method":"pi_set_time","params":[{"lat":"private","Lon":99,"gain":80,"extra":9007199254740993},null,{"tail":9007199254740993}],"Params":null,"extra":9007199254740993}` + "\r\n")
	out, event := protectSite(input)
	want := `{"ID":99,"Method":"pi_set_time","Params":null,"extra":9007199254740993,"id":9007199254740993,"method":"set_setting","params":[{"Lon":99,"extra":9007199254740993,"gain":80},null,{"tail":9007199254740993}]}` + "\r\n"
	if string(out) != want || event == nil || event.rejection != nil {
		t.Fatalf("output=%s event=%+v want=%s", out, event, want)
	}
	for _, raw := range []string{
		`{"method":"get_device_state","Method":"set_user_location","params":{}}`,
		`{"method":"set_setting","params":{"Lat":1,"Lon":2,"Time_zone":"private"}}`,
		`{"method":"set_setting","params":[{},null,{"tail":9007199254740993}]}`,
	} {
		line := []byte(raw + "\r\n")
		if out, event := protectSite(line); !bytes.Equal(out, line) || event != nil {
			t.Fatalf("unrelated request changed: %s event=%+v", out, event)
		}
	}
	for _, params := range []string{`null`, `[]`, `[null]`, `[1]`, `[[{}]]`, `"private"`, `7`, `true`} {
		out, event := protectSite([]byte(`{"id":9,"method":"set_setting","params":` + params + "}\r\n"))
		if len(out) != 0 || event == nil || event.reason != "invalid_params" || !bytes.Contains(event.rejection, []byte(`"code":-32602`)) {
			t.Fatalf("invalid params=%s output=%s event=%+v", params, out, event)
		}
	}
}

// Retain the original JSON port matrix as well as the binary port regressions.
func TestSiteJSONPortsTCP(t *testing.T) {
	for _, port := range []uint16{4040, 4350, 4400, 4500, 4700, 4701, 4702, 4720, 4800, 4801, 4804} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			client, remote := siteTransportPair(t, port)
			request := `{"id":9,"method":"pi_set_time","params":[{"time_zone":"America/Los_Angeles","year":2001}]}` + "\r\n"
			bad := `{"id":10,"method":"pi_set_time\u0000suffix","params":{}}` + "\r\n"
			if _, err := io.WriteString(client, request+bad); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(remote)
			inspected := port == 4400 || port == 4700 || port == 4701 || port == 4702
			want := request + bad
			if inspected {
				want = ""
			}
			if err != nil || string(got) != want {
				t.Fatalf("upstream=%s err=%v want=%s", got, err, want)
			}
			if err := remote.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err = io.ReadAll(client)
			want = ""
			if inspected {
				want = `{"jsonrpc":"2.0","id":9,"error":{"code":-32602,"message":"site_protected"}}` + "\r\n" +
					`{"jsonrpc":"2.0","id":10,"error":{"code":-32600,"message":"request rejected by site protection"}}` + "\r\n"
			}
			if err != nil || string(got) != want {
				t.Fatalf("downstream=%s err=%v want=%s", got, err, want)
			}
		})
	}
}
