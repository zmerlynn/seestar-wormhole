package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// Runtime bind forms, state paths, and startup diagnostics remain bounded and safe.
func TestOSBindForms(t *testing.T) {
	for _, bind := range []string{"", "127.0.0.1", "127.1", "localhost", "::", "::1", "0.0.0.0", "::ffff:127.0.0.1", "::1%lo"} {
		t.Run(bind, func(t *testing.T) {
			setValidCommandEnvironment(t)
			// Ask the standard library first; unsupported host forms are not an acceptance claim.
			l, e := net.Listen("tcp", net.JoinHostPort(bind, "0"))
			if e != nil {
				t.Skipf("OS does not accept form: %v", e)
			}
			l.Close()
			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, []string{"--bind", bind, "--port", availableCommandPort(t) + ":445"}...), &out, &logs)
			if !errors.Is(err, cloud.ErrConfig) {
				t.Fatalf("OS-accepted bind rejected before credentials: %v log=%s", err, &logs)
			}
			t.Logf("OS bind accepted; reached credential boundary; stdout=%q", out.String())
		})
	}
}
func TestRuntimeFailures(t *testing.T) {
	for _, kind := range []string{"occupied-tcp", "occupied-udp"} {
		t.Run(kind, func(t *testing.T) {
			setValidCommandEnvironment(t)
			args := []string{"--bind", "0.0.0.0", "--port", availableCommandPort(t) + ":13459", "--log-format=text", "--log-level=error"}
			option := "--bind"
			switch kind {
			case "occupied-tcp":
				l, e := net.Listen("tcp", "0.0.0.0:0")
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
				args = []string{"--bind", "0.0.0.0", "--port", strconv.Itoa(l.Addr().(*net.TCPAddr).Port) + ":13459"}
				option = "--bind"
			case "occupied-udp":
				l, e := net.ListenPacket("udp4", "0.0.0.0:4720")
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
				args = append(args, "--lan-discovery")
				option = "--bind"
			}
			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, args...), &out, &logs)
			var rec map[string]any
			if e := json.Unmarshal(logs.Bytes(), &rec, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatal(e, &logs)
			}
			if rec["option"] != option || rec["rule"] == nil || rec["help"] != programName+" proxy --help" || out.Len() != 0 {
				t.Errorf("missing safe diagnostic: stdout=%q log=%s", out.String(), &logs)
			}
			assertNoSensitiveErrorDetails(t, err, "PRIVATE_FILE_PATH", "PRIVATE_TARGET", "13459")
		})
	}
}
