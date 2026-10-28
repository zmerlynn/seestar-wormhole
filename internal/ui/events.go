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
	l.failed = l.appendLine([]byte(line+"\n")) != nil
}

// Disk history is opaque: append without reading or projecting prior content.
// Refuse unsafe metadata and oversized prior files; retain just one rotation.
func (l *eventLog) appendLine(line []byte) error {
	path := filepath.Join(l.storage.root, "gui.log")
	f, err := privatefile.Open(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > logLimit {
		return errStorage
	}
	if info.Size()+int64(len(line)) > logLimit {
		rotated := path + ".1"
		old, err := privatefile.OpenRead(rotated)
		if err == nil {
			info, statErr := old.Stat()
			old.Close()
			if statErr != nil || info.Size() > logLimit {
				return errStorage
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		f = nil
		if err := privatefile.Replace(path, rotated); err != nil {
			return err
		}
		f, err = privatefile.Open(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND)
		if err != nil {
			return err
		}
	}
	// privatefile.Open uses native handles on Windows; position append there too.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	n, err := f.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	err = f.Close()
	f = nil
	return err
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
