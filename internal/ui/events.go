package ui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

const logLimit = 64 << 10
const eventLimit = 80

// Only fixed, user-facing lifecycle categories enter either log. No dependency
// error, settings, address, account, control capability, or payload is accepted.
type eventLog struct {
	mu      sync.Mutex
	storage *storage
	recent  []string
	failed  bool
}

func safeEvent(text string) bool {
	switch text {
	case "Application opened.", "Application could not start.", "Connecting to your telescope…", "Connected to your telescope", "Waiting for an app to connect", "Telescope is in use. Disconnect the other remote session. Retrying automatically.", "Connection lost. Retrying automatically.", "Releasing idle telescope session…", "Waiting for session release before retrying…", "Stopped", "Stopped. You can use remote mode in the Seestar app again.", "Settings saved.", "Password saved to keychain. It will be checked when you connect.", "Password removed from keychain and this app.":
		return true
	}
	for _, code := range []string{"email", "password", "idle", "ports", "bind", "discovery_bind", "discovery", "http_permission", "http_conflict", "port_conflict", "signin", "selection", "serial", "model", "release", "storage", "password_save", "password_remove", "password_read", "password_missing", "keychain_timeout", "connection"} {
		if text == errorCopy(code) {
			return true
		}
	}
	return false
}
func (l *eventLog) add(text string) {
	if l == nil || !safeEvent(text) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recent) > 0 && strings.HasSuffix(l.recent[len(l.recent)-1], "\t"+text) {
		return
	}
	line := time.Now().UTC().Format(time.RFC3339) + "\tlifecycle\t" + text
	l.recent = append(l.recent, line)
	if len(l.recent) > eventLimit {
		l.recent = append([]string(nil), l.recent[len(l.recent)-eventLimit:]...)
	}
	if l.storage == nil {
		return
	}
	old, err := l.readFile()
	if err != nil {
		l.failed = true
		return
	}
	b := append(old, []byte(line+"\n")...)
	if len(b) > logLimit {
		if err := l.storage.writeBytes("gui.log.1", old); err != nil {
			l.failed = true
			return
		}
		b = []byte(line + "\n")
	}
	l.failed = l.storage.writeBytes("gui.log", b) != nil
}
func (l *eventLog) readFile() ([]byte, error) {
	f, err := privatefile.OpenRead(filepath.Join(l.storage.root, "gui.log"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, logLimit+1))
	if err != nil {
		return nil, err
	}
	// Reject oversized or modified history instead of copying arbitrary text.
	if len(b) > logLimit {
		return nil, errStorage
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[1] != "lifecycle" || !safeEvent(fields[2]) {
			return nil, errStorage
		}
		if _, err := time.Parse(time.RFC3339, fields[0]); err != nil {
			return nil, errStorage
		}
	}
	return b, nil
}
func (l *eventLog) snapshot() ([]string, string, bool) {
	if l == nil {
		return []string{}, "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := ""
	if l.storage != nil {
		path = filepath.Join(l.storage.root, "gui.log")
	}
	return append([]string{}, l.recent...), path, l.failed
}
