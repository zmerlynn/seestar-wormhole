package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestModelOption(t *testing.T) {
	for _, command := range []string{"proxy", "pull"} {
		for _, value := range []string{"S30", "S50 Pro", "unknown synthetic model", strings.Repeat("m", 4096)} {
			_, err := parse([]string{command, "--serial=synthetic-a", "--model=" + value, "--version"}, io.Discard)
			if err != nil {
				t.Fatalf("%s rejected bounded model: %v", command, err)
			}
		}
		for _, value := range []string{" ", "private\rmodel", "private\nmodel", "private\x00model", strings.Repeat("m", 4097)} {
			var out, logs bytes.Buffer
			err := run(t.Context(), []string{command, "--serial=synthetic-a", "--model=" + value, "--version"}, &out, &logs)
			var option *diagnostic.OptionError
			if !errors.As(err, &option) || option.Option != "--model" || option.Rule != "use a nonblank device model of at most 4096 bytes without NUL, CR or LF" {
				t.Fatalf("%s error=%v, want model validation", command, err)
			}
			if (strings.TrimSpace(value) != "" && strings.Contains(logs.String(), value)) || out.Len() != 0 {
				t.Fatal("invalid model value escaped diagnostics")
			}
		}
		// An empty model retains serial-only behavior.
		if _, err := parse([]string{command, "--model=", "--version"}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModelOptionStartupWiring(t *testing.T) {
	for _, command := range []string{"proxy", "pull"} {
		called := false
		var out, logs bytes.Buffer
		args := []string{command, "--serial=PRIVATE_SERIAL", "--model=PRIVATE_MODEL"}
		if command == "proxy" {
			args = append(args, "--bind=127.0.0.1", "--port", availableCommandPort(t)+":13459", "--idle=2m")
		} else {
			args = append(args, t.TempDir())
		}
		err := runWithSession(t.Context(), args, &out, &logs, keylog.Open, func(_ context.Context, cfg proxy.Config) (commandSession, error) {
			called = true
			if cfg.Serial != "PRIVATE_SERIAL" || cfg.Model != "PRIVATE_MODEL" {
				t.Fatal("selected pair not forwarded to session configuration")
			}
			return nil, errors.New("synthetic startup refusal")
		})
		if err == nil || !called {
			t.Fatalf("startup error=%v called=%v", err, called)
		}
		if strings.Contains(logs.String()+out.String(), "PRIVATE_") {
			t.Fatal("input pair escaped diagnostics")
		}
	}
}

func TestModelOptionViaBoundary(t *testing.T) {
	for _, value := range []string{"", "S30"} {
		_, err := parse([]string{"pull", "--via=127.0.0.1:4445", "--model=" + value, "--version"}, io.Discard)
		var option *diagnostic.OptionError
		if !errors.As(err, &option) || option.Option != "--model" || option.Rule != "cannot be used with --via" {
			t.Fatalf("error=%v, want --model/--via exclusion", err)
		}
	}
}
