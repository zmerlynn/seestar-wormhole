package main

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFlagDefaultsAndMappings(t *testing.T) {
	o, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil || o.bind != defaultBind || o.idle != 0 || len(o.ports) != 10 || o.ports[1] != (mapping{4445, 445}) {
		t.Fatalf("defaults %+v %v", o, err)
	}
	o, err = parse(append([]string{"proxy"}, []string{"--port", "1445:445", "--port", "8080:80", "--idle", "2m", "--log-format", "text"}...), io.Discard)
	if err != nil || len(o.ports) != 10 || o.ports[1].local != 1445 || o.idle != 2*time.Minute {
		t.Fatalf("custom %+v %v", o, err)
	}
	for _, args := range [][]string{{"--port", "65536:80"}, {"--port", "80:0"}, {"--port", "80:80:80"}, {"--port", "80:80", "--port", "80:445"}, {"--idle", "-1s"}, {"--log-format", "xml"}, {"password"}} {
		if _, err := parse(append([]string{"proxy"}, args...), io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestUTCLogFormat(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var b bytes.Buffer
		l := newLogger(format, &b)
		r := slog.NewRecord(time.Date(2026, 10, 10, 12, 34, 56, 123456789, time.FixedZone("offset", 3600)), slog.LevelInfo, "event", 0)
		if err := l.Handler().Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "2026-10-10T11:34:56.123Z") {
			t.Fatal(b.String())
		}
		if format == "json" {
			var v map[string]any
			if json.Unmarshal(b.Bytes(), &v, json.MatchCaseInsensitiveNames(true)) != nil {
				t.Fatal("invalid JSON")
			}
		}
	}
}
func TestVersionWithoutCredentialsOrNetworking(t *testing.T) {
	var b bytes.Buffer
	if err := run(context.Background(), append([]string{"proxy"}, []string{"--version"}...), &b, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(b.String()) != versionString() {
		t.Fatal(b.String())
	}
}

func TestDefaultDeviceProfile(t *testing.T) {
	o, _ := parse(append([]string{"proxy"}, nil...), io.Discard)
	want := mappings{{4700, 4700}, {4445, 445}, {8080, 80}, {4350, 4350}, {4554, 4554}, {4555, 4555}, {4801, 4801}, {4800, 4800}, {4804, 4804}, {4361, 4361}}
	if !reflect.DeepEqual(o.ports, want) {
		t.Fatalf("profile %v, want %v", o.ports, want)
	}
}
func TestContainerArgumentsRetainBind(t *testing.T) {
	b, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	var entry []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ENTRYPOINT ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "ENTRYPOINT ")), &entry, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := parse(append([]string{"proxy"}, entry[2:]...), io.Discard); err != nil {
		t.Fatalf("container arguments: err=%v", err)
	}
	args := append(entry[2:], "--idle", "2m", "--port", "1445:445")
	o, err := parse(append([]string{"proxy"}, args...), io.Discard)
	if err != nil || o.bind != "0.0.0.0" || o.stateDir != "/state" {
		t.Fatalf("custom container flags lost bind default: %+v %v", o, err)
	}
	args = append(args, "--bind", "127.0.0.1", "--state-dir", "")
	o, err = parse(append([]string{"proxy"}, args...), io.Discard)
	if err != nil || o.bind != "127.0.0.1" || o.stateDir != "" {
		t.Fatal("explicit override ignored")
	}
}

func TestContainerRunsAsNonRoot(t *testing.T) {
	// scripts/test-container.sh supplies the built final image's Config.User.
	// Inspecting the Containerfile text misses later USER overrides and stages.
	user, ok := os.LookupEnv("SEESTAR_CONTAINER_USER")
	if !ok {
		t.Skip("run scripts/test-container.sh to inspect built runtime images")
	}
	ids := strings.Split(user, ":")
	if len(ids) != 2 {
		t.Fatalf("effective Config.User=%q, want nonzero numeric UID:GID", user)
	}
	for _, id := range ids {
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil || n == 0 {
			t.Fatalf("effective Config.User=%q, want nonzero numeric UID:GID", user)
		}
	}
}
