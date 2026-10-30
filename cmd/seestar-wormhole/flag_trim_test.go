package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPortOverrides(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		count int
		http  uint16
	}{
		{nil, 10, 8080}, {[]string{"--port", "80:80"}, 10, 80},
		{[]string{"--port", "0:80", "--port", "0:445"}, 8, 0},
		{[]string{"--port", "9000:9001"}, 11, 8080},
	} {
		o, err := parse(append([]string{"proxy"}, tc.args...), io.Discard)
		if err != nil || len(o.ports) != tc.count {
			t.Fatalf("args %v: mappings %v error %v", tc.args, o.ports, err)
		}
		var http uint16
		for _, p := range o.ports {
			if p.Device == 80 {
				http = p.Local
			}
		}
		if http != tc.http {
			t.Fatalf("HTTP local %d want %d", http, tc.http)
		}
	}
	for _, args := range [][]string{{"--port", "81:80", "--port", "82:80"}, {"--port", "0:80", "--port", "81:80"}, {"--port", "4700:80"}} {
		if _, err := parse(append([]string{"proxy"}, args...), io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestLANHTTPWarning(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		warning bool
	}{
		{nil, true}, {[]string{"--port", "80:80"}, false}, {[]string{"--port", "0:80"}, true}, {[]string{"--port", "80:9000", "--port", "0:80"}, true},
	} {
		o, err := parse(append([]string{"proxy"}, tc.args...), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		warnLANHTTP(newLogger("json", &logs), o.ports)
		if strings.Contains(logs.String(), "album/image views need -p 80:80") != tc.warning {
			t.Fatal("incorrect HTTP warning")
		}
		if strings.Count(logs.String(), "\n") > 1 {
			t.Fatal("repeated warning")
		}
	}
}

func TestNewOptionsAndRetiredFlags(t *testing.T) {
	o, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil || o.stateDir != "" || o.serial != "" {
		t.Fatal("unexpected new defaults")
	}
	o, err = parse(append([]string{"proxy"}, []string{"--state-dir", "", "--serial", "synthetic-serial"}...), io.Discard)
	if err != nil || o.stateDir != "" || o.serial != "synthetic-serial" {
		t.Fatal("explicit option value lost")
	}
	var help bytes.Buffer
	_, err = parse(append([]string{"proxy"}, []string{"--help"}...), &help)
	if !errors.Is(err, errHelp) || !strings.Contains(help.String(), "default: memory only; UI state root with --config") {
		t.Fatal("missing visible state default")
	}
	for _, name := range []string{"--app-ports", "--lan-template"} {
		var out, logs bytes.Buffer
		err := run(context.Background(), append([]string{"proxy"}, []string{name + "=PRIVATE_SENTINEL"}...), &out, &logs)
		assertStartupOptionFailure(t, err, &out, &logs, "arguments", "use only supported options", "PRIVATE_SENTINEL", name)
	}
}
