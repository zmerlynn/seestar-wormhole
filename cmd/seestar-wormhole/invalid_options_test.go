package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"strings"
	"testing"
)

// Invalid options and values produce safe, attributed diagnostics.
func TestInvalidOptions(t *testing.T) {
	type test struct {
		name, option, rule string
		args, secrets      []string
	}
	tests := []test{
		{"unknown-name", "arguments", "use only supported options", []string{"--credential_PRIVATE_MARKER=/private/SECRET_MARKER"}, []string{"PRIVATE_MARKER", "SECRET_MARKER"}},
		{"invalid-syntax", "arguments", "use only supported options", []string{"---credential_PRIVATE_MARKER"}, []string{"PRIVATE_MARKER"}},
		{"malformed-name", "arguments", "use only supported options", []string{"-=credential_PRIVATE_MARKER"}, []string{"PRIVATE_MARKER"}},
		{"positional-after-dash", "arguments", "positional arguments are not allowed", []string{"--", "credential_PRIVATE_MARKER"}, []string{"PRIVATE_MARKER"}},
		{"bool-next-token", "arguments", "positional arguments are not allowed", []string{"--lan-discovery", "PRIVATE_MARKER"}, []string{"PRIVATE_MARKER"}},
		{"duplicate", "--port", "local ports must be unique", []string{"--port=12347:13459", "--port=12347:14561"}, []string{"12347", "13459", "14561"}},
		{"app-duplicate", "--port", "local ports must be unique", []string{"--port=4700:14561"}, []string{"14561"}},
		{"port-overflow", "--port", "local port must be 0..65535", []string{"--port=987654321:14561"}, []string{"987654321", "14561"}},
		{"device-overflow", "--port", "device port must be 1..65535", []string{"--port=14561:987654321"}, []string{"987654321", "14561"}},
		{"bad-bind-discovery", "--bind", "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0", []string{"--lan-discovery", "--bind=HOST_PRIVATE_MARKER"}, []string{"PRIVATE_MARKER"}},
	}
	for _, name := range []string{"idle"} {
		for _, value := range []string{"PRIVATE_MARKER", "99999999999999999999999h", "\"PRIVATE_MARKER\""} {
			tests = append(tests, test{name + value, "--" + name, "use a duration such as 2m or 0", []string{"--" + name + "=" + value}, []string{value}})
		}
		tests = append(tests, test{name + "negative", "--" + name, "duration must be nonnegative", []string{"--" + name, "-123456789ns"}, []string{"123456789"}})
	}
	for _, value := range []string{"PRIVATE_MARKER", "99999999999999999999999", "12.3456789"} {
		tests = append(tests, test{"int" + value, "--pause-threshold", "use an integer byte count", []string{"--pause-threshold=" + value}, []string{value}})
	}
	for _, value := range []string{"-123456789", "987654321"} {
		tests = append(tests, test{"int-range" + value, "--pause-threshold", "byte count must be 2..8388608", []string{"--pause-threshold=" + value}, []string{value}})
	}
	for _, name := range []string{"lan-discovery", "version"} {
		tests = append(tests, test{name + "bool", "--" + name, "use a boolean value", []string{"--" + name + "=PRIVATE_MARKER"}, []string{"PRIVATE_MARKER"}})
	}
	for _, name := range []string{"bind", "port", "idle", "pause-threshold", "log-format", "log-level", "serial", "state-dir"} {
		for _, prefix := range []string{"--"} {
			tests = append(tests, test{prefix + name + "missing", "--" + name, "requires a value", []string{prefix + name}, nil})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, tc.args...), &out, &logs)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			var record map[string]any
			if e := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatalf("want single JSON record: %v, %q", e, logs.String())
			}
			if record["option"] != tc.option || record["rule"] != tc.rule || record["phase"] != "arguments" || record["reason"] != "invalid_option" || record["help"] != "seestar-wormhole proxy --help" {
				t.Errorf("got %v, want %s: %s", record, tc.option, tc.rule)
			}
			if out.Len() != 0 {
				t.Errorf("unexpected stdout %q", out.String())
			}
			var chain func(error)
			chain = func(e error) {
				if e == nil {
					return
				}
				for _, secret := range tc.secrets {
					if strings.Contains(e.Error(), secret) {
						t.Errorf("returned error leaks %q: %v", secret, e)
					}
				}
				switch x := e.(type) {
				case interface{ Unwrap() []error }:
					for _, child := range x.Unwrap() {
						chain(child)
					}
				case interface{ Unwrap() error }:
					chain(x.Unwrap())
				}
			}
			chain(err)
			for _, secret := range tc.secrets {
				if strings.Contains(logs.String(), secret) || strings.Contains(out.String(), secret) {
					t.Errorf("output leaks %q", secret)
				}
			}
		})
	}
	t.Logf("checked %d independently specified invalid invocations", len(tests))
}

func TestHelpOutput(t *testing.T) {
	for _, help := range []string{"-h", "--help"} {
		for _, prefix := range [][]string{nil, {"--bind=HOST_PRIVATE_MARKER", "--pause-threshold=1234567", "--idle=1234h"}} {
			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, append(prefix, help)...), &out, &logs)
			if !errors.Is(err, errHelp) || logs.Len() != 0 {
				t.Fatalf("help error %v logs %q", err, logs.String())
			}
			if strings.Contains(out.String(), "(default local:device)") {
				t.Fatal("port help presents a placeholder as the default mapping")
			}
			for _, v := range []string{"PRIVATE_MARKER", "PATH_MARKER", "12.3456789", "23.4567891", "1234567", "1234h"} {
				if strings.Contains(out.String(), v) {
					t.Errorf("help leaks %q", v)
				}
			}
		}
	}
}

func FuzzConfiguration(f *testing.F) {
	for _, s := range []string{"", "secret password", "\x00", "line\nfeed", "=--help", "91.234567,123.456789", "/private/credentials"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip()
		}
		private := "FUZZ_SECRET_MARKER:" + value
		for _, name := range []string{"port", "idle", "pause-threshold", "lan-discovery", "version", "log-format", "log-level", "unknown"} {
			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, []string{"--" + name + "=" + private}...), &out, &logs)
			if err == nil {
				t.Fatalf("accepted private invalid input for %s", name)
			}
			var record map[string]any
			if json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)) != nil || record["option"] == nil || record["rule"] == nil {
				t.Fatalf("missing safe diagnostic: %q", logs.String())
			}
			if strings.Contains(logs.String(), "FUZZ_SECRET_MARKER") || out.Len() != 0 {
				t.Fatalf("leaked private input: %q %q", out.String(), logs.String())
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				if strings.Contains(e.Error(), "FUZZ_SECRET_MARKER") {
					t.Fatalf("returned error leak: %v", e)
				}
			}
		}
	})
}
