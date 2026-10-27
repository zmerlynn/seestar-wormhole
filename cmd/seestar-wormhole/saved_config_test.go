package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

func savedConfigRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	s := ui.Settings{Version: 1, Bind: "127.0.0.2", Idle: "2m", Serial: "SYNTHETIC_SERIAL", Model: "S50", Ports: []string{"1445:445", "0:80"}, Email: "saved@example.invalid"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSavedConfigOptions(t *testing.T) {
	root := savedConfigRoot(t)
	for _, override := range []bool{false, true} {
		args := []string{"proxy", "--config", "--state-dir", root}
		if override {
			args = append(args, "--bind=127.0.0.1", "--idle=0", "--serial=", "--model=", "-p", "2445:445", "-p", "8081:80")
		}
		o, err := parse(args, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		bind, idle, serial, model := "127.0.0.2", 2*time.Minute, "SYNTHETIC_SERIAL", "S50"
		smb, http := uint16(1445), uint16(0)
		if override {
			bind, idle, serial, model, smb, http = "127.0.0.1", 0, "", "", 2445, 8081
		}
		if o.bind != bind || o.idle != idle || o.serial != serial || o.model != model || o.stateDir != root || o.savedEmail != "saved@example.invalid" {
			t.Fatalf("options not loaded/overridden: %+v", o)
		}
		got := map[uint16]uint16{}
		for _, p := range o.ports {
			got[p.Device] = p.Local
		}
		if got[445] != smb || got[80] != http || got[4700] != 4700 {
			t.Fatalf("ports=%v", got)
		}
	}
}

func TestSavedConfigCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		key    string
		keyErr error
		email  string
		calls  int
		fail   bool
	}{
		{name: "keychain hit", key: "saved-password", email: "saved@example.invalid", calls: 1},
		{name: "email override", env: map[string]string{"SEESTAR_EMAIL": "override@example.invalid"}, key: "saved-password", email: "override@example.invalid", calls: 1},
		{name: "env password", env: map[string]string{"SEESTAR_PASSWORD": "env-password"}, email: "saved@example.invalid"},
		{name: "env empty wins", env: map[string]string{"SEESTAR_PASSWORD": ""}, email: "saved@example.invalid", fail: true},
		{name: "miss", keyErr: errors.New("PRIVATE backend miss"), email: "saved@example.invalid", calls: 1, fail: true},
		{name: "unavailable", keyErr: errors.New("PRIVATE backend unavailable"), email: "saved@example.invalid", calls: 1, fail: true},
		{name: "empty entry", email: "saved@example.invalid", calls: 1, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			lookup := func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }
			creds, err := savedCredentials(t.Context(), "saved@example.invalid", lookup, func(_ context.Context, email string) (string, error) {
				calls++
				if email != tc.email {
					t.Fatalf("keychain email=%s", email)
				}
				return tc.key, tc.keyErr
			})
			if calls != tc.calls {
				t.Fatalf("calls=%d want=%d", calls, tc.calls)
			}
			if tc.fail {
				if !errors.Is(err, cloud.ErrConfig) || commandExitCode(options{command: commandProxy}, err) != 2 {
					t.Fatalf("err=%v", err)
				}
				var detail *argumentError
				if tc.name != "env empty wins" && (!errors.As(err, &detail) || detail.Rule != "Password unavailable: set SEESTAR_PASSWORD or save a password in the OS keychain using the UI") {
					t.Fatalf("message=%q", err)
				}
				return
			}
			password := tc.key
			if v, ok := tc.env["SEESTAR_PASSWORD"]; ok {
				password = v
			}
			want, _ := cloud.EmailCredentials(tc.email, password)
			if err != nil || !reflect.DeepEqual(creds, want) {
				t.Fatalf("credential mismatch: err=%v", err)
			}
		})
	}
}

func TestSavedConfigRuntimeHandoff(t *testing.T) {
	for _, source := range []string{"environment", "keychain"} {
		t.Run(source, func(t *testing.T) {
			root := savedConfigRoot(t)
			// Setenv registers restoration even when the variable was already set.
			t.Setenv("SEESTAR_EMAIL", "")
			if err := os.Unsetenv("SEESTAR_EMAIL"); err != nil {
				t.Fatal(err)
			}
			password := "synthetic-" + source + "-password"
			t.Setenv("SEESTAR_PASSWORD", password)
			if source == "keychain" {
				if err := os.Unsetenv("SEESTAR_PASSWORD"); err != nil {
					t.Fatal(err)
				}
			}
			o, err := parse([]string{"proxy", "--config", "--state-dir", root}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			construction := errors.New("synthetic session construction boundary")
			keychainCalls, sessionCalls := 0, 0
			var logs bytes.Buffer
			err = runParsedWithConfigRuntime(t.Context(), o, io.Discard, &logs,
				func(context.Context, proxy.Config) (commandSession, error) {
					t.Fatal("config dispatch used environment-only session constructor")
					return nil, nil
				},
				func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
				listenCommandDiscovery, configRuntime{
					keychainPassword: func(_ context.Context, email string) (string, error) {
						keychainCalls++
						if source != "keychain" || email != "saved@example.invalid" {
							t.Fatal("unexpected keychain lookup")
						}
						return password, nil
					},
					newSession: func(_ context.Context, cfg proxy.Config, credentials cloud.Credentials) (*proxy.Session, error) {
						sessionCalls++
						want, err := cloud.EmailCredentials("saved@example.invalid", password)
						if err != nil || credentials != want || cfg.StateDir != root {
							t.Fatal("saved email, chosen password or state root did not reach session construction")
						}
						return nil, construction
					},
				})
			wantKeychainCalls := 0
			if source == "keychain" {
				wantKeychainCalls = 1
			}
			if !errors.Is(err, construction) || sessionCalls != 1 || keychainCalls != wantKeychainCalls {
				t.Fatalf("session calls=%d keychain calls=%d err=%v logs=%s", sessionCalls, keychainCalls, err, &logs)
			}
		})
	}
}

func TestSavedConfigMissingPasswordBeforeStartup(t *testing.T) {
	root := savedConfigRoot(t)
	o, err := parse([]string{"proxy", "--config", "--state-dir", root}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// All external operations are replaced; a provider miss returns immediately.
	start := time.Now()
	_, err = savedCredentials(t.Context(), o.savedEmail, func(string) (string, bool) { return "", false }, func(context.Context, string) (string, error) { return "", errors.New("PRIVATE backend error") })
	if commandExitCode(o, err) != 2 || time.Since(start) > time.Second {
		t.Fatalf("err=%v", err)
	}
	var logs bytes.Buffer
	logCommandArgumentError(o, &logs, err)
	if !strings.Contains(logs.String(), `"rule":"Password unavailable: set SEESTAR_PASSWORD or save a password in the OS keychain using the UI"`) || strings.Contains(logs.String(), "PRIVATE") {
		t.Fatalf("logs=%s", &logs)
	}
}

func TestSavedConfigRefusesUnknownVersion(t *testing.T) {
	root := savedConfigRoot(t)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"version":999,"email":"PRIVATE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	err := runWithListener(t.Context(), []string{"proxy", "--config", "--state-dir", root}, &out, &logs, func(context.Context, proxy.Config) (commandSession, error) {
		t.Fatal("config refusal constructed session")
		return nil, nil
	}, func(string, string) (net.Listener, error) { t.Fatal("config refusal opened listener"); return nil, nil })
	if commandExitCode(options{command: commandProxy}, err) != 2 || out.Len() != 0 || !strings.Contains(logs.String(), `"option":"--config"`) || strings.Contains(logs.String(), "PRIVATE") {
		t.Fatalf("err=%v logs=%s", err, &logs)
	}
}

func TestSavedConfigCredentialRefusalBeforeListeners(t *testing.T) {
	root := savedConfigRoot(t)
	t.Setenv("SEESTAR_PASSWORD", "") // Explicit invalid input must never consult the real keychain.
	var logs bytes.Buffer
	err := runWithListener(t.Context(), []string{"proxy", "--config", "--state-dir", root}, io.Discard, &logs, func(context.Context, proxy.Config) (commandSession, error) {
		t.Fatal("credential refusal constructed session")
		return nil, nil
	}, func(string, string) (net.Listener, error) {
		t.Fatal("credential refusal opened listener")
		return nil, nil
	})
	if commandExitCode(options{command: commandProxy}, err) != 2 || !strings.Contains(logs.String(), `"option":"--config"`) {
		t.Fatalf("err=%v logs=%s", err, &logs)
	}
}
