package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
)

func TestStartupDiagnostics(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "sentinel-password")

	args := []string{"--port", "14700:4700", "--port", "14445:445", "--port", "18080:80", "--log-format", "json"}
	var logs, out bytes.Buffer
	if err := run(context.Background(), append([]string{"proxy"}, args...), &out, &logs); err == nil {
		t.Fatal("missing credentials accepted")
	}
	if !strings.Contains(logs.String(), `"phase":"arguments"`) || !strings.Contains(logs.String(), `"option":"SEESTAR_EMAIL"`) {
		t.Fatalf("missing diagnostic: %s", &logs)
	}
	for _, secret := range []string{"sentinel@example.invalid", "sentinel-password", "sentinel-model"} {
		if strings.Contains(logs.String()+out.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
}

func TestListenerAndArgumentDiagnostics(t *testing.T) {
	l, err := net.Listen("tcp", net.JoinHostPort(defaultBind, "14700"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--port", "14700:4700"}, "invalid_option"},
		{[]string{"--idle", "sentinel-password"}, "invalid_option"},
	} {
		var out, logs bytes.Buffer
		if run(context.Background(), append([]string{"proxy"}, tc.args...), &out, &logs) == nil {
			t.Fatal("bad startup accepted")
		}
		if !strings.Contains(logs.String(), tc.want) {
			t.Fatalf("missing %s: %s", tc.want, &logs)
		}
		if strings.Contains(logs.String(), `"port":`) || strings.Contains(logs.String(), `"device_port":`) {
			t.Fatalf("configuration failure retained private mapping: %s", &logs)
		}
		if strings.Contains(out.String()+logs.String(), "sentinel-password") {
			t.Fatal("flag parser leaked value")
		}
	}
}

func TestDebugFlag(t *testing.T) {
	if _, err := parse(append([]string{"proxy"}, []string{"--log-level", "debug"}...), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := parse(append([]string{"proxy"}, []string{"--log-level", "sentinel-password"}...), &bytes.Buffer{}); err == nil {
		t.Fatal("bad level accepted")
	}
}
