package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type siteWireSession struct {
	endpoint string
	done     chan struct{}
	ports    chan uint16
	t        *testing.T
}

func (s *siteWireSession) Close() error               { close(s.done); return nil }
func (s *siteWireSession) Done() <-chan struct{}      { return s.done }
func (s *siteWireSession) DeviceDetail() cloud.Detail { return cloud.Detail{} }
func (s *siteWireSession) DialPort(ctx context.Context, port uint16) (net.Conn, error) {
	s.ports <- port
	return (&net.Dialer{}).DialContext(ctx, "tcp", s.endpoint)
}

type siteWireFrame struct {
	raw                     string
	method                  string
	location, clock, reject bool
}

// Synthetic shapes plus the review's exact object-time reproduction. The oracle
// below checks the wire independently of the production filter.
func siteWireFrames() []siteWireFrame {
	frames := []siteWireFrame{}
	for _, method := range []string{"set_user_location", "pi_set_time", "set_setting"} {
		for _, object := range []string{
			`{"lat":-71.23456,"lon":81.98765,"time_zone":"America/Los_Angeles","year":2001,"mon":2,"day":3,"hour":4,"min":5,"sec":6,"force":false,"extra":{"n":9007199254740993}}`,
			`{"lat":"-71.23456","lon":"81.98765","time_zone":"America/Los_Angeles","year":"2001","mon":"2","day":"3","hour":"4","min":"5","sec":"6","force":true,"extra":{"n":9007199254740993}}`,
		} {
			for _, wrap := range []bool{false, true} {
				params := object
				if wrap {
					params = "[" + object + `,null,{"tail":9007199254740993}]`
				}
				frames = append(frames, siteWireFrame{raw: fmt.Sprintf(`{"id":%d,"method":%q,"params":%s,"envelope_extra":9007199254740993}`+"\r\n", 100+len(frames), method, params), method: method, location: method == "set_setting", clock: method == "set_setting", reject: method != "set_setting"})
			}
		}
		for _, params := range []string{"null", "[]", "[null]", "[1]", `"private-params"`, "7", "[[{}]]"} {
			frames = append(frames, siteWireFrame{raw: fmt.Sprintf(`{"id":%d,"method":%q,"params":%s}`+"\r\n", 100+len(frames), method, params), reject: true})
		}
		frames = append(frames, siteWireFrame{raw: fmt.Sprintf(`{"id":%d,"method":%q}`+"\r\n", 100+len(frames), method), reject: true})
	}
	frames = append(frames,
		siteWireFrame{raw: `{"id":32,"method":"pi_set_time","params":{"time_zone":"America/Los_Angeles","year":2001,"mon":2,"day":3,"hour":4,"min":5,"sec":6}}` + "\r\n", method: "pi_set_time", reject: true},
		siteWireFrame{raw: `{"id":33,"method":"set_user_location","params":{}}` + "\r\n", method: "set_user_location", reject: true},
		siteWireFrame{raw: `{"id":34,"method":"pi_set_time","params":[{}]}` + "\r\n", method: "pi_set_time", reject: true},
		siteWireFrame{raw: `{"id":35,"method":"set_setting","params":{"lat":"10","lang":"en"}}` + "\r\n", method: "set_setting", location: true},
		siteWireFrame{raw: `{"id":36,"method":"set_setting","params":[{"lon":20,"lang":"en"},false]}` + "\r\n", method: "set_setting", location: true},
		siteWireFrame{raw: `{"id":37,"method":"set_setting","params":{"time_zone":"America/Los_Angeles"}}` + "\r\n", method: "set_setting", reject: true},
		siteWireFrame{raw: `{"id":38,"method":"set_setting","params":[{"lang":"en","force":true},null]}` + "\r\n"},
		siteWireFrame{raw: `{"id":39,"method":"get_device_state","params":{"keys":["location_lon_lat"]}}` + "\r\n"},
		siteWireFrame{raw: `{"id":40,"method":"pi_set_time","params":{broken}}` + "\r\n", reject: true},
		siteWireFrame{raw: `{"id":41,"method":"set_user_location","params":{}} {}` + "\r\n", reject: true},
		siteWireFrame{raw: `[{"id":42,"method":"pi_set_time","params":{}},{"id":43,"method":"set_user_location","params":{}}]` + "\r\n", reject: true},
		siteWireFrame{raw: strings.Repeat(" ", proxy.MaxSiteLine) + `{"method":"pi_set_time","params":{}}` + "\r\n", reject: true},
		siteWireFrame{raw: `{"id":44,"method":"set_user_location","params":{}}` + "\n", reject: true},
		siteWireFrame{raw: `{"id":45,"method":"pi_set_time","params":{}}`, reject: true},
	)
	return frames
}

func TestSiteCLIToFakeDevice(t *testing.T) {
	for _, mode := range []struct {
		name  string
		flags []string
		local string
		port  uint16
	}{
		{"default", nil, "4700", 4700},
		{"remap", []string{"-p14700:4700"}, "14700", 4700},
		{"swap", []string{"-p14700:4700", "-p4700:445"}, "14700", 4700},
		{"non-control", []string{"-p14700:4700", "-p4700:445"}, "4700", 445},
	} {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/split=%t", mode.name, split), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				device, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer device.Close()
				s := &siteWireSession{endpoint: device.Addr().String(), done: make(chan struct{}), ports: make(chan uint16, 1), t: t}
				ready := make(chan map[string]string, 1)
				listeners := make(map[string]string)
				args := []string{"proxy", "--log-level=debug"}
				args = append(args, mode.flags...)
				var logs bytes.Buffer
				done := make(chan error, 1)
				go func() {
					done <- runWithListener(ctx, args, io.Discard, &logs, keylog.Open,
						func(context.Context, proxy.Config) (commandSession, error) { ready <- listeners; return s, nil },
						func(_, address string) (net.Listener, error) {
							_, port, err := net.SplitHostPort(address)
							if err != nil {
								return nil, err
							}
							l, err := net.Listen("tcp", "127.0.0.1:0")
							if err == nil {
								listeners[port] = l.Addr().String()
							}
							return l, err
						})
				}()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
					for _, secret := range []string{"12.345", "67.891", "Pacific/Kiritimati", "-71.23456", "81.98765", "America/Los_Angeles", "private-params"} {
						if strings.Contains(logs.String(), secret) {
							t.Errorf("log leaks %q", secret)
						}
					}
					if mode.port == 4700 {
						rejects := 0
						for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
							var event struct{ Msg, Level string }
							if json.Unmarshal(line, &event, json.MatchCaseInsensitiveNames(true)) == nil && (event.Msg == "site_reject" || event.Msg == "site_protected") {
								rejects++
								if event.Msg == "site_reject" && event.Level != "WARN" {
									t.Errorf("rejection level=%s", event.Level)
								}
							}
						}
						want := 0
						for _, f := range siteWireFrames() {
							if f.reject || f.location || f.clock {
								want++
							}
						}
						if rejects != want {
							t.Errorf("rejection logs=%d want=%d", rejects, want)
						}
					}
				}()
				select {
				case listeners = <-ready:
					select {
					case port := <-s.ports:
						t.Fatalf("startup dialed port %d before first client", port)
					default:
					}
				case <-ctx.Done():
					t.Fatal("startup timeout")
				}
				client, err := net.Dial("tcp", listeners[mode.local])
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				client.SetDeadline(time.Now().Add(10 * time.Second))
				remote, err := device.Accept()
				if err != nil {
					t.Fatal(err)
				}
				defer remote.Close()
				remote.SetDeadline(time.Now().Add(10 * time.Second))
				if port := <-s.ports; port != mode.port {
					t.Fatalf("device port=%d want=%d", port, mode.port)
				}
				var request strings.Builder
				frames := siteWireFrames()
				for _, f := range frames {
					request.WriteString(f.raw)
				}
				parts := []string{request.String()}
				if split {
					parts = nil
					for _, f := range frames {
						n := len(f.raw)
						parts = append(parts, f.raw[:7], f.raw[7:n-1], f.raw[n-1:])
					}
				}
				for _, part := range parts {
					if _, err := io.WriteString(client, part); err != nil {
						t.Fatal(err)
					}
				}
				client.(*net.TCPConn).CloseWrite()
				got, err := io.ReadAll(remote)
				if err != nil {
					t.Fatal(err)
				}
				inspected := mode.port == 4700
				if !inspected {
					if string(got) != request.String() {
						t.Fatal("transparent upstream changed bytes")
					}
				} else {
					lines := bytes.Split(bytes.TrimSuffix(got, []byte("\r\n")), []byte("\r\n"))
					index := 0
					for _, f := range frames {
						if f.reject {
							continue
						}
						if index >= len(lines) {
							t.Fatal("missing forwarded frame")
						}
						assertSiteWireFrame(t, f, lines[index])
						index++
					}
					if index != len(lines) {
						t.Fatalf("extra device frames=%d", len(lines)-index)
					}
				}
				// Exactly one client dial; no extra startup channel before or after admission.
				select {
				case port := <-s.ports:
					t.Fatalf("extra device dial %d", port)
				default:
				}
				// Setters in device traffic stay byte-identical, including fragments.
				response := `{"id":900,"method":"pi_set_time","params":{"time_zone":"America/Los_Angeles"}}` + "\r\n"
				for _, part := range []string{response[:13], response[13 : len(response)-1], response[len(response)-1:]} {
					if _, err := io.WriteString(remote, part); err != nil {
						t.Fatal(err)
					}
				}
				remote.(*net.TCPConn).CloseWrite()
				downstream, err := io.ReadAll(client)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.HasSuffix(downstream, []byte(response)) {
					t.Fatal("device response changed")
				}
				errors := bytes.TrimSuffix(downstream, []byte(response))
				if !inspected {
					if len(errors) != 0 {
						t.Fatal("unexpected local response")
					}
					return
				}
				replies := bytes.Split(bytes.TrimSuffix(errors, []byte("\r\n")), []byte("\r\n"))
				index := 0
				for _, f := range frames {
					if !f.reject {
						continue
					}
					if index >= len(replies) {
						t.Fatal("missing JSON-RPC error")
					}
					var response struct {
						JSONRPC string
						ID      jsontext.Value
						Error   struct {
							Code    int
							Message string
						}
					}
					if json.Unmarshal(replies[index], &response, json.MatchCaseInsensitiveNames(true)) != nil || response.JSONRPC != "2.0" || response.Error.Code >= 0 || (response.Error.Message != "site_protected" && response.Error.Message != "request rejected by site protection") {
						t.Fatalf("invalid JSON-RPC error: %s", replies[index])
					}
					var original map[string]jsontext.Value
					if json.Unmarshal([]byte(f.raw), &original, json.MatchCaseInsensitiveNames(true)) == nil && strings.HasSuffix(f.raw, "\r\n") && len(f.raw) <= proxy.MaxSiteLine {
						if string(response.ID) != string(original["id"]) {
							t.Errorf("error ID=%s want=%s", response.ID, original["id"])
						}
					} else if string(response.ID) != "null" {
						t.Errorf("unparseable error ID=%s", response.ID)
					}
					index++
				}
				if index != len(replies) {
					t.Fatalf("extra local errors=%d", len(replies)-index)
				}
			})
		}
	}
}

func assertSiteWireFrame(t *testing.T, f siteWireFrame, line []byte) {
	t.Helper()
	if f.method == "" {
		if string(line)+"\r\n" != f.raw {
			t.Fatal("nonsetter changed")
		}
		return
	}
	var a, b map[string]jsontext.Value
	if json.Unmarshal([]byte(f.raw), &a, json.MatchCaseInsensitiveNames(true)) != nil || json.Unmarshal(line, &b, json.MatchCaseInsensitiveNames(true)) != nil {
		t.Fatal("invalid forwarded JSON")
	}
	if !bytes.Equal(a["id"], b["id"]) || !bytes.Equal(a["method"], b["method"]) || !bytes.Equal(a["envelope_extra"], b["envelope_extra"]) {
		t.Fatal("envelope changed")
	}
	old, rewritten := a["params"], b["params"]
	if old[0] == '[' {
		var x, y []jsontext.Value
		if json.Unmarshal(old, &x, json.MatchCaseInsensitiveNames(true)) != nil || json.Unmarshal(rewritten, &y, json.MatchCaseInsensitiveNames(true)) != nil || len(x) != len(y) {
			t.Fatal("array shape changed")
		}
		for i := 1; i < len(x); i++ {
			if !bytes.Equal(x[i], y[i]) {
				t.Fatal("extra array element changed")
			}
		}
		old, rewritten = x[0], y[0]
	} else if rewritten[0] != '{' {
		t.Fatal("object shape changed")
	}
	var x, y map[string]jsontext.Value
	json.Unmarshal(old, &x, json.MatchCaseInsensitiveNames(true))
	json.Unmarshal(rewritten, &y, json.MatchCaseInsensitiveNames(true))
	for key, value := range x {
		if f.location && (key == "lat" || key == "lon") || f.clock && (key == "time_zone" || key == "year" || key == "mon" || key == "day" || key == "hour" || key == "min" || key == "sec") {
			continue
		}
		if !bytes.Equal(value, y[key]) {
			t.Errorf("unconsumed field %s changed", key)
		}
	}
	for _, key := range []string{"lat", "lon", "time_zone", "year", "mon", "day", "hour", "min", "sec"} {
		if _, ok := y[key]; ok {
			t.Errorf("protected field %s reached device", key)
		}
	}
}
