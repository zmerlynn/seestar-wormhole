package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Invalid invocations identify configuration errors without exposing values.
func TestArgumentDiagnostic(t *testing.T) {
	for _, args := range [][]string{{"--port", "invalid:80"}} {
		t.Run(args[0], func(t *testing.T) {
			var logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, args...), &bytes.Buffer{}, &logs)
			if err == nil {
				t.Fatal("bad configuration was accepted")
			}
			_, reason := diagnostic.Fields(err, "arguments")
			if reason == "invalid_arguments" {
				t.Errorf("configuration error does not identify the invalid option: %s", logs.String())
			}
		})
	}
}

func TestArgumentRulesAndRedaction(t *testing.T) {
	for _, tt := range []struct {
		name, option, rule string
		args, private      []string
	}{
		{"mapping shape", "--port", "use local:device ports", []string{"--port", "secret-port"}, []string{"secret-port"}},
		{"local port", "--port", "local port must be 0..65535", []string{"--port", "secret-port:80"}, []string{"secret-port"}},
		{"device port", "--port", "device port must be 1..65535", []string{"--port", "80:secret-port"}, []string{"secret-port"}},
		{"duplicate port", "--port", "local ports must be unique", []string{"--port", "80:80", "--port", "80:445"}, nil},
		{"app conflict", "--port", "local ports must be unique", []string{"--port", "4700:8080"}, nil},
		{"duration syntax", "--idle", "use a duration such as 2m or 0", []string{"--idle", "secret-duration"}, []string{"secret-duration"}},
		{"idle range", "--idle", "duration must be nonnegative", []string{"--idle", "-1s"}, nil},
		{"stats syntax", "--stats-interval", "use a duration such as 2m or 0", []string{"--stats-interval", "secret-duration"}, []string{"secret-duration"}},
		{"stats range", "--stats-interval", "duration must be nonnegative", []string{"--stats-interval", "-1s"}, nil},
		{"integer syntax", "--pause-threshold", "use an integer byte count", []string{"--pause-threshold", "secret-count"}, []string{"secret-count"}},
		{"pressure range", "--pause-threshold", "byte count must be 2..8388608", []string{"--pause-threshold", "8388609"}, nil},
		{"format", "--log-format", "choose json or text", []string{"--log-format", "secret-format"}, []string{"secret-format"}},
		{"level", "--log-level", "choose debug, info, warn or error", []string{"--log-level", "secret-level"}, []string{"secret-level"}},
		{"discovery bind", "--bind", "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0", []string{"--lan-discovery", "--bind", "private-host"}, []string{"private-host"}},
		{"boolean", "--lan-discovery", "use a boolean value", []string{"--lan-discovery=secret-boolean"}, []string{"secret-boolean"}},
		{"missing value", "--keylog", "requires a value", []string{"--keylog"}, nil},
		{"unknown option", "arguments", "use only supported options", []string{"--private-secret=/private/path"}, []string{"private-secret", "/private/path"}},
		{"positional", "arguments", "positional arguments are not allowed", []string{"private-password"}, []string{"private-password"}},
	} {
		if runtime.GOOS == "windows" && tt.name == "discovery bind" {
			tt.option, tt.rule = "--lan-discovery", "LAN discovery is unsupported on Windows"
		}
		t.Run(tt.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, tt.args...), &out, &logs)
			if err == nil {
				t.Fatal("invalid arguments accepted")
			}
			var record struct{ Phase, Reason, Option, Rule, Help string }
			if e := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatalf("want one JSON diagnostic, got %q: %v", logs.String(), e)
			}
			if record.Phase != "arguments" || record.Reason != "invalid_option" || record.Option != tt.option || record.Rule != tt.rule || record.Help != "seestar-wormhole proxy --help" {
				t.Fatalf("got %+v, want option %q and rule %q with help hint", record, tt.option, tt.rule)
			}
			if out.Len() != 0 {
				t.Fatalf("unexpected argument output %q", out.String())
			}
			for _, private := range tt.private {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("diagnostic leaked %q", private)
				}
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					if strings.Contains(cause.Error(), private) {
						t.Fatalf("returned error leaked %q", private)
					}
				}
			}
		})
	}
}

func TestArgumentHelpAndMissingValues(t *testing.T) {
	for _, help := range []string{"-h", "--help"} {
		var out, logs bytes.Buffer
		if err := run(context.Background(), append([]string{"proxy"}, []string{help}...), &out, &logs); !errors.Is(err, errHelp) || logs.Len() != 0 || !strings.Contains(out.String(), "--bind") {
			t.Fatalf("%s: want clean help, got err=%v output=%q logs=%q", help, err, out.String(), logs.String())
		}
	}
	for _, name := range []string{"bind", "port", "idle", "stats-interval", "pause-threshold", "log-format", "log-level", "keylog", "trace", "serial", "state-dir"} {
		var logs bytes.Buffer
		err := run(context.Background(), append([]string{"proxy"}, []string{"--" + name}...), io.Discard, &logs)
		if err == nil || !strings.Contains(logs.String(), `"option":"--`+name+`"`) || !strings.Contains(logs.String(), `"rule":"requires a value"`) {
			t.Fatalf("%s: missing value diagnostic = %q, err=%v", name, logs.String(), err)
		}
	}
}

func TestArgumentMappingLimit(t *testing.T) {
	var args []string
	for i := range 118 {
		args = append(args, "--port", fmt.Sprintf("%d:%d", 10000+i, 20000+i))
	}
	if o, err := parse(append([]string{"proxy"}, args...), io.Discard); err != nil || len(o.ports) != 128 {
		t.Fatalf("mappings %d error %v", len(o.ports), err)
	}
	args = append(args, "--port", "15000:25000")
	var logs bytes.Buffer
	if err := run(context.Background(), append([]string{"proxy"}, args...), io.Discard, &logs); err == nil || !strings.Contains(logs.String(), `"option":"--port"`) {
		t.Fatal("mapping limit", err)
	}
}
