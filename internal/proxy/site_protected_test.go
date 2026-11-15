package proxy

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
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

func TestSiteSettingPassThroughTCP(t *testing.T) {
	for _, tc := range []struct{ name, params string }{
		{"mixed", `,"params":{ "lat":35,"lon":-105,"time_zone":"private-zone","year":2001,"mon":1,"day":2,"hour":3,"min":4,"sec":5,"gain":80 }`},
		{"site only", `,"params":{"lat":35,"lon":-105,"time_zone":"private-zone"}`},
		{"clock only", `,"params":{"year":2001,"mon":1,"day":2,"hour":3,"min":4,"sec":5}`},
		{"array", `,"params":[{"lat":null,"lon":{},"time_zone":false,"year":[],"gain":80},null,{"tail":9007199254740993}]`},
		{"unknown", `,"params":{ "z":"<>&\u2028\u2029", "a":1.2300e+04, "esc":"\u0061", "Lat":1 }`},
		{"missing", ``},
		{"null", `,"params":null`},
		{"empty array", `,"params":[]`},
		{"null element", `,"params":[null]`},
		{"number element", `,"params":[1]`},
		{"nested array", `,"params":[[{}]]`},
		{"string", `,"params":"private"`},
		{"number", `,"params":7`},
		{"boolean", `,"params":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, remote := siteTransportPair(t, 4700)
			request := `{ "id":9007199254740993, "method":"set_setting", "Method":"pi_set_time", "future":{"z":-0,"a":1e+09}` + tc.params + " }\r\n"
			if _, err := io.WriteString(client, request); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(remote)
			if err != nil || string(got) != request {
				t.Fatalf("upstream=%q err=%v want=%q", got, err, request)
			}
			remote.(*net.TCPConn).CloseWrite()
			got, err = io.ReadAll(client)
			if err != nil || len(got) != 0 {
				t.Fatalf("unexpected local reply=%s err=%v", got, err)
			}
		})
	}
}

func TestSiteProtectedLogsAndIDs(t *testing.T) {
	for _, tc := range []struct{ request, method, id, params string }{
		{`{"id":115,"method":"set_user_location","params":{"lat":12.345,"lon":67.891,"extra-key":"extra-value"}}`, "set_user_location", "115", `{"lat":12.345,"lon":67.891,"unknown":["extra-key"]}`},
		{`{"id":"client-id","method":"pi_set_time","params":[{"time_zone":"Etc/GMT+7","year":2001,"mon":1,"day":2,"hour":3,"min":4,"sec":5}]}`, "pi_set_time", `"client-id"`, `[{"day":2,"hour":3,"min":4,"mon":1,"sec":5,"time_zone":"Etc/GMT+7","year":2001}]`},
		{`{"id":1e400,"method":"pi_set_time","params":{}}`, "pi_set_time", `"1e400"`, `{}`},
		{`{"id":9223372036854775808,"method":"set_user_location","params":{}}`, "set_user_location", `"9223372036854775808"`, `{}`},
		{`{"id":5,"method":"scope_set_time","params":["clock-value"]}`, "scope_set_time", "5", `["clock-value"]`},
		{`{"id":6,"method":"scope_set_location","params":{"latitude":12.345,"longitude":-67.891}}`, "scope_set_location", "6", `{"latitude":12.345,"longitude":-67.891}`},
		{`{"id":7,"method":"scope_set_time","params":{"offset":"-07:00","utc":"2001-02-03T04:05:06Z"}}`, "scope_set_time", "7", `{"offset":"-07:00","utc":"2001-02-03T04:05:06Z"}`},
		{`{"id":8,"method":"set_user_location","params":[81.98765,-71.23456,false]}`, "set_user_location", "8", `[81.98765,-71.23456,false]`},
		{`{"id":9,"method":"set_user_location"}`, "set_user_location", "9", ``},
		// Unknown values never appear, however deep, and a known name with a
		// nested value is listed by name only.
		{`{"id":10,"method":"set_user_location","params":{"lat":1,"password":"SECRET-PASSWORD","nested":{"token":"SECRET-TOKEN"},"lon":{"key":"SECRET-KEY"}}}`, "set_user_location", "10", `{"lat":1,"unknown":["lon","nested","password"]}`},
		{`{"id":11,"method":"pi_set_time","params":[{"year":2001,"auth":{"license":"SECRET-LICENSE"}},["SECRET-SESSION"],{"time_zone":["SECRET-CONNECTION"]}]}`, "pi_set_time", "11", `[{"unknown":["auth"],"year":2001},[],{"unknown":["time_zone"]}]`},
	} {
		t.Run(tc.method+tc.id, func(t *testing.T) {
			var out, reply, logs bytes.Buffer
			err := copySite(&out, strings.NewReader(tc.request+"\r\n"), slog.New(slog.NewJSONHandler(&logs, nil)), &reply)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				ID jsontext.Value `json:"id"`
			}
			if err := json.Unmarshal([]byte(tc.request), &request); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"site_protected"}}`+"\r\n", request.ID)
			if out.Len() != 0 || reply.String() != want {
				t.Fatalf("forwarded=%q reply=%q want=%q", out.Bytes(), reply.Bytes(), want)
			}
			var record struct {
				Msg, Level, Method, Reason, Params string
				ID                                 jsontext.Value
			}
			if bytes.Count(logs.Bytes(), []byte("\n")) != 1 || json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)) != nil || record.Msg != "site_protected" || record.Level != "WARN" || record.Reason != "site_protected" || record.Method != tc.method || string(record.ID) != tc.id || record.Params != tc.params {
				t.Fatalf("log=%s want method=%s id=%s params=%s", logs.Bytes(), tc.method, tc.id, tc.params)
			}
			if bytes.Contains(logs.Bytes(), []byte("SECRET")) {
				t.Fatalf("log has an unknown value: %s", logs.Bytes())
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
