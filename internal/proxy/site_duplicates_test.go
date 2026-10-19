package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
)

func TestSiteDuplicateNamesTCP(t *testing.T) {
	frames := []string{
		`{"method":"pi_set_time","method":"get_setting"}`,
		`{"method":"get_setting","method":"pi_set_time"}`,
		`{"method":"pi_set_time","method":null}`,
		`{"method":"pi_set_time","method":42}`,
		`{"method":"pi_set_time","method":{}}`,
		`{"method":"set_user_location","method":null}`,
		`{"method":"scope_set_location","method":null}`,
		`{"method":"scope_set_time","method":null}`,
		`{"method":"set_setting","params":{"lat":1,"gain":80},"method":null}`,
		`{"method":"set_setting","params":{"gain":80},"params":{"lat":1}}`,
		`{"id":1,"id":2,"method":"get_setting"}`,
		`{"method":"get_setting","params":{"future":[{"x":1,"x":2}]}}`,
		`{"method":"get_setting","metho\u0064":"pi_set_time"}`,
		`{"method":"set_setting","params":{"lat":1,"la\u0074":2,"gain":80}}`,
		`{"method":"get_setting","future":{"a":1,"a":2}}`,
	}
	for _, port := range []uint16{4400, 4700, 4701, 4702} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			client, remote := siteTransportPair(t, port)
			for _, frame := range frames {
				if _, err := io.WriteString(client, frame+"\r\n"); err != nil {
					t.Fatal(err)
				}
			}
			const ordinary = "{ \"id\":9007199254740993, \"method\":\"get_setting\", \"future\":{\"A\":1,\"a\":2} }\r\n"
			if _, err := io.WriteString(client, ordinary); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(remote)
			if err != nil || string(got) != ordinary {
				t.Fatalf("device received %q, %v; want ordinary frame only", got, err)
			}
			// Duplicate replies and invalid UTF-8 remain byte-transparent device output.
			const deviceReply = "{\"result\":1,\"result\":2,\"binary\":\"\xff\"}\r\n"
			if _, err := io.WriteString(remote, deviceReply); err != nil {
				t.Fatal(err)
			}
			remote.(*net.TCPConn).CloseWrite()
			got, err = io.ReadAll(client)
			answer := []byte("{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"request rejected by site protection\"}}\r\n")
			want := append(bytes.Repeat(answer, len(frames)), []byte(deviceReply)...)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("client received %q, %v; want %q", got, err, want)
			}
		})
	}
}
