package ui

import (
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

const logLimit = 64 << 10
const eventLimit = 80

// Only fixed, user-facing lifecycle categories enter either log. No dependency
// error, settings, address, account, control capability, or payload is accepted.
type event struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
	Code    string    `json:"code"`
	Phase   string    `json:"phase"`
	Reason  string    `json:"reason"`
}

type eventLog struct {
	mu      sync.Mutex
	storage *storage
	recent  []event
	failed  bool
}

func eventCode(text string) string {
	switch text {
	case "Application opened.", "Application could not start.", "Connecting to your telescope…", "Connected to your telescope", "Waiting for an app to connect", "Telescope is in use. Disconnect the other remote session. Retrying automatically.", "Connection lost. Retrying automatically.", "Releasing idle telescope session…", "Waiting for session release before retrying…", "Stopped", "Stopped. The telescope may take a minute to release the session.", "Settings saved.", "Password saved to keychain. It will be checked when you connect.", "Password removed from keychain and this app.":
		return "lifecycle"
	}
	for _, code := range []string{"email", "password", "idle", "ports", "ports_empty", "bind", "discovery_bind", "discovery", "http_permission", "http_conflict", "port_conflict", "signin", "selection", "serial", "model", "release", "storage", "password_save", "password_remove", "password_read", "password_missing", "keychain_timeout", "connection", "busy"} {
		if text == errorCopy(code) {
			return code
		}
	}
	return ""
}
func (l *eventLog) add(text string) { l.addFailure(text, nil) }

func (l *eventLog) addFailure(text string, err error) {
	code := eventCode(text)
	if l == nil || code == "" {
		return
	}
	entry := event{Time: time.Now().UTC(), Message: text, Code: code}
	if err != nil {
		entry.Phase, entry.Reason = diagnostic.Fields(err, "connect")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recent) > 0 {
		last := l.recent[len(l.recent)-1]
		if last.Message == entry.Message && last.Phase == entry.Phase && last.Reason == entry.Reason {
			return
		}
	}
	l.recent = append(l.recent, entry)
	if len(l.recent) > eventLimit {
		l.recent = append([]event(nil), l.recent[len(l.recent)-eventLimit:]...)
	}
	if l.storage == nil {
		return
	}
	line, err := json.Marshal(entry)
	l.failed = err != nil || l.appendLine(append(line, '\n')) != nil
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
func (l *eventLog) snapshot() ([]event, string, bool) {
	if l == nil {
		return []event{}, "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := ""
	if l.storage != nil {
		path = filepath.Join(l.storage.root, "gui.log")
	}
	return append([]event{}, l.recent...), path, l.failed
}
