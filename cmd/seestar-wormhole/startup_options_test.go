package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// Validate options that are resolved during startup I/O as well as during parsing.
func TestAttackRuntimeOptions(t *testing.T) {
	for _, tc := range []struct {
		name, option, rule, secret string
		args                       []string
	}{
		{"invalid bind", "--bind", "use an OS-supported bind address with available local ports", "HOST_PRIVATE_MARKER", []string{"--bind", "HOST_PRIVATE_MARKER/invalid", "--port", "12347:13459"}},
		{"trace path", "--trace", "use a new writable trace file", "PATH_PRIVATE_MARKER", []string{"--trace", "/nonexistent/PATH_PRIVATE_MARKER/trace"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, suffix := range [][]string{nil, {"--log-format=text", "--log-level=debug", "--keylog=/nonexistent/PRIVATE_KEYLOG_PATH"}} {
				var out, logs bytes.Buffer
				args := append(append([]string(nil), tc.args...), suffix...)
				err := run(context.Background(), append([]string{"proxy"}, args...), &out, &logs)
				if err == nil {
					t.Fatal("invalid runtime configuration accepted")
				}
				assertStartupOptionFailure(t, err, &out, &logs, tc.option, tc.rule, tc.secret, "12347", "13459", "PRIVATE_KEYLOG_PATH")
			}
		})
	}
}

func TestAttackStateDirectoryReturnedError(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "PRIVATE_CREDENTIAL_DIRECTORY")
	if err := os.WriteFile(privatePath, []byte("file blocks directory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEESTAR_EMAIL", "test@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")

	var out, logs bytes.Buffer
	err := run(context.Background(), append([]string{"proxy"}, []string{"--state-dir", privatePath, "--port=14387:445", "--log-format=text", "--log-level=debug", "--keylog=/nonexistent/PRIVATE_KEYLOG_PATH"}...), &out, &logs)
	if err == nil {
		t.Fatal("invalid credential state directory accepted")
	}
	assertStartupOptionFailure(t, err, &out, &logs, "--state-dir", "use a private writable state directory and installation identity file", privatePath, "PRIVATE_PASSWORD", "PRIVATE_KEYLOG_PATH", "14387", "file blocks directory")
	if !errors.Is(err, cloud.ErrConfig) {
		t.Fatal("state failure lost ErrConfig")
	}
}

func assertStartupOptionFailure(t *testing.T, err error, out, logs *bytes.Buffer, option, rule string, secrets ...string) {
	t.Helper()
	var record map[string]any
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	for _, line := range lines[:len(lines)-1] {
		var warning map[string]any
		if e := json.Unmarshal(line, &warning); e != nil || warning["level"] != "WARN" || (warning["rule"] != "startup_diagnostics_lost" && warning["rule"] != "keylog_loss") {
			t.Fatalf("unexpected startup diagnostic: %s", line)
		}
	}
	if e := json.Unmarshal(lines[len(lines)-1], &record, json.MatchCaseInsensitiveNames(true)); e != nil {
		t.Fatalf("want one JSON diagnostic, got %q: %v", logs.String(), e)
	}
	if record["option"] != option || record["rule"] != rule || record["phase"] != "arguments" || record["reason"] != "invalid_option" || record["help"] != "seestar-wormhole proxy --help" {
		t.Errorf("got %v, want %s: %s with argument help", record, option, rule)
	}
	if record["cleanup_reason"] != "none" {
		t.Errorf("cleanup_reason=%v want none", record["cleanup_reason"])
	}
	for key := range record {
		switch key {
		case "time", "level", "msg", "option", "rule", "phase", "reason", "help", "cleanup_reason":
		default:
			t.Errorf("configuration diagnostic carries incidental attribute %q", key)
		}
	}
	if out.Len() != 0 {
		t.Errorf("unexpected stdout %q", out.String())
	}
	var visit func(error)
	visit = func(e error) {
		if e == nil {
			return
		}
		for _, secret := range secrets {
			if strings.Contains(e.Error(), secret) {
				t.Errorf("returned error chain leaks %q through %T: %v", secret, e, e)
			}
		}
		switch e := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range e.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			visit(e.Unwrap())
		}
	}
	visit(err)
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log leaks %q: %s", secret, logs)
		}
	}
}
