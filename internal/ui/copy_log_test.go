package ui

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
)

// lockedBuffer collects writes from any goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The copier's warnings go to the command's log sink as JSON records,
// never to its output. Copying here fails at its folder, before any dial.
func TestCopyLogsGoToTheSink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	root := filepath.Join(t.TempDir(), "state")
	st, err := openStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	const config = `{"version":1,"bind":"0.0.0.0","idle":"0","discovery":true,"route":"lan","lan_address":"192.0.2.10:4450"}`
	err = st.writeBytes("config.json", []byte(config))
	st.directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(folder, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, logs lockedBuffer
	err = runUIPresented(t.Context(), root, &out, &logs, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(ctx context.Context, url string, closeWindow func(), _ <-chan struct{}) error {
		defer func() {
			closeWindow()
			<-ctx.Done()
		}()
		base, token, _ := strings.Cut(url, "/#")
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
		defer client.CloseIdleConnections()
		post := func(path, body string) {
			t.Helper()
			req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
			req.Header.Set("Origin", base)
			req.Header.Set("X-Seestar-Token", token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("%s: %d", path, resp.StatusCode)
			}
		}
		at, _ := json.Marshal(folder)
		post("/copy/api/settings", `{"folder":`+string(at)+`,"interval":"5","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`)
		post("/copy/api/copy", `{"action":"start"}`)
		for deadline := time.Now().Add(10 * time.Second); !strings.Contains(logs.String(), "copying stopped") && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}, &copyapp.Native{})
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	line, _, _ := strings.Cut(logs.String(), "\n")
	if err := json.Unmarshal([]byte(line), &record); err != nil || record.Level != "ERROR" || record.Msg != "copying stopped" {
		t.Errorf("log sink: %s (%v)", logs.String(), err)
	}
	if strings.Contains(out.String(), "copying") {
		t.Errorf("the copier wrote to the output: %s", out.String())
	}
}

// The app's copier keeps its history in seestar-copy-cli's state folder,
// under the user's config folder, so the two share one history and lock
// per destination. Saving settings dials nothing.
func TestCopyStateIsShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	root := filepath.Join(t.TempDir(), "state")
	var history string
	err := runUIPresented(t.Context(), root, io.Discard, io.Discard, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(ctx context.Context, url string, closeWindow func(), _ <-chan struct{}) error {
		defer func() {
			closeWindow()
			<-ctx.Done()
		}()
		base, token, _ := strings.Cut(url, "/#")
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
		defer client.CloseIdleConnections()
		send := func(method, path, body string) []byte {
			t.Helper()
			req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
			req.Header.Set("Origin", base)
			req.Header.Set("X-Seestar-Token", token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
			}
			return b
		}
		at, _ := json.Marshal(filepath.Join(t.TempDir(), "Seestar"))
		send("POST", "/copy/api/settings", `{"folder":`+string(at)+`,"interval":"5","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`)
		var status struct {
			History string `json:"history"`
		}
		if err := json.Unmarshal(send("GET", "/copy/api/status", ""), &status); err != nil {
			t.Fatal(err)
		}
		history = status.History
		return nil
	}, &copyapp.Native{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "seestar-copy") + string(filepath.Separator); !strings.HasPrefix(history, want) {
		t.Errorf("copy history %q, want it under %q", history, want)
	}
	if _, err := os.Stat(filepath.Join(root, "seestar-copy")); !os.IsNotExist(err) {
		t.Errorf("a copy state folder appeared beside the app's settings: %v", err)
	}
}
