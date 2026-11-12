package ui

import (
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
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

// Only fixed lifecycle messages, including the selected display label, and
// the fixed file-tool lines enter either log. No dependency error, account,
// control capability, path, file name or payload is accepted. Source is
// "connection" for a connection line; a file-tool line also has its op, and
// its count of failed files when there are any.
type event struct {
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Op      string    `json:"op,omitempty"`
	Message string    `json:"message"`
	Code    string    `json:"code"`
	Phase   string    `json:"phase"`
	Reason  string    `json:"reason"`
	Count   int       `json:"count,omitzero"`
}

type eventLog struct {
	mu      sync.Mutex // Protects only the in-memory projection.
	diskMu  sync.Mutex // Orders publication and disk appends; never held by status.
	storage *storage
	recent  []event
	failed  bool
	// lastConnection is the last connection line, which a repeat of it
	// collapses into; hasConnection says there is one.
	lastConnection event
	hasConnection  bool
	// failing holds each failing file-tool operation, and lastSeq the
	// newest outcome seen of each operation, failing or not.
	failing map[opKey]*failState
	lastSeq map[opKey]uint64
}

func eventCode(text, telescope string, catalogs ...cloudhttp.Catalog) string {
	if len(catalogs) == 1 && text == catalogs[0].Message() {
		return "lifecycle"
	}
	// Dynamic messages require explicit selection context and an exact template.
	if telescope != "" {
		for _, state := range []string{"Ready", "Waiting for a local client", "Busy — waiting for the other session", "Seestar not reachable", "Reconnecting", "Releasing idle session", "Reconnecting — waiting for cleanup/retry"} {
			if text == telescopeText(state, telescope) {
				return "lifecycle"
			}
		}
	}
	switch text {
	case "App opened.", "App could not start.", copyRefusedEvent, "Connecting to your Seestar…", "Connected to your Seestar", "Waiting for an app to connect", "Seestar is occupied. Retrying automatically.", "Seestar isn't reachable. Check this computer's internet connection and make sure Seestar is awake and connected to Wi-Fi. Retrying automatically.", "Connection interrupted. Retrying automatically.", "Releasing idle Seestar session…", "Waiting before retrying the connection…", "Stopped", "Stopped. Seestar may still be releasing the session.", "Settings saved.", "Password saved to keychain. Checked when you connect.", "Password removed from keychain and this app.":
		return "lifecycle"
	}
	for _, code := range []string{"email", "password", "idle", "ports", "ports_empty", "bind", "bind_empty", "discovery_bind", "discovery", "http_permission", "http_conflict", "port_conflict", "signin", "selection", "selection_multiple", "serial", "model", "release", "storage", "password_save", "password_remove", "password_read", "password_missing", "keychain_timeout", "route_locked", "folder_claim", "lan_address", "lan_unreachable", "connection", "busy"} {
		if text == errorCopy(code) {
			return code
		}
	}
	return ""
}
func (l *eventLog) add(text string) { l.addFailure(text, nil) }

func (l *eventLog) addFailure(text string, err error) {
	if l == nil {
		return
	}
	l.diskMu.Lock()
	defer l.diskMu.Unlock()
	l.mu.Lock()
	entry := l.publishLocked(text, err, "")
	l.mu.Unlock()
	l.write(entry)
}

// addScan records a line about a scan of this network under code, one of
// the scan codes. Its text names the telescopes, their addresses and the
// reply count.
func (l *eventLog) addScan(code, text string) {
	if l == nil {
		return
	}
	l.diskMu.Lock()
	defer l.diskMu.Unlock()
	l.mu.Lock()
	// Every connect scans, so a repeat of the last connection line, as
	// a not-seen one is on each pass, collapses into it.
	entry := l.publishCodedLocked(text, nil, code, true)
	l.mu.Unlock()
	l.write(entry)
}

// The scan codes: a scan of the page's, a typed address asked, a scan that
// couldn't run, and the copier's scans for a saved telescope: found at a
// new address and saved, found there but not saved, found but not
// connecting, not seen, and another telescope at the saved address.
const (
	scanListed     = "scan_listed"
	scanProbed     = "scan_probed"
	scanFailedCode = "scan_failed"
	scanMoved      = "scan_moved"
	scanUnsaved    = "scan_moved_unsaved"
	scanUnreached  = "scan_unreached"
	scanNotSeen    = "scan_not_seen"
	scanOther      = "scan_other_telescope"
)

// publishLocked records an immutable event in memory, without disk I/O.
func (l *eventLog) publishLocked(text string, err error, telescope string, catalogs ...cloudhttp.Catalog) *event {
	code := eventCode(text, telescope, catalogs...)
	if code == "" {
		return nil
	}
	return l.publishCodedLocked(text, err, code, len(catalogs) == 0)
}

// publishCodedLocked is publishLocked with its code chosen. collapse drops
// a repeat of the last connection line.
func (l *eventLog) publishCodedLocked(text string, err error, code string, collapse bool) *event {
	entry := event{Time: time.Now().UTC(), Source: diagnostic.SourceConnection, Message: text, Code: code}
	if err != nil {
		entry.Phase, entry.Reason = diagnostic.Fields(err, "connect")
	}
	// A repeat collapses into the last connection line, whatever file-tool
	// lines came between.
	if collapse && l.hasConnection {
		last := l.lastConnection
		if last.Message == entry.Message && last.Phase == entry.Phase && last.Reason == entry.Reason {
			return nil
		}
	}
	l.lastConnection, l.hasConnection = entry, true
	l.appendLocked(entry)
	return &entry
}

// appendLocked adds entry to the in-memory trail, keeping its newest
// eventLimit lines. The caller holds mu.
func (l *eventLog) appendLocked(entry event) {
	l.recent = append(l.recent, entry)
	if len(l.recent) > eventLimit {
		l.recent = append([]event(nil), l.recent[len(l.recent)-eventLimit:]...)
	}
}

// The caller holds diskMu, preserving the memory publication order on disk.
func (l *eventLog) write(entry *event) {
	if entry == nil || l.storage == nil {
		return
	}
	line, err := json.Marshal(entry)
	failed := err != nil || l.appendLine(append(line, '\n')) != nil
	l.mu.Lock()
	l.failed = failed
	l.mu.Unlock()
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
	events, _, path, failed := l.trail()
	return events, path, failed
}

// trail returns the in-memory trail, oldest first, the failing file-tool
// operations, the log's path and whether its last append failed, all from
// one moment.
func (l *eventLog) trail() ([]event, []opFailure, string, bool) {
	if l == nil {
		return []event{}, []opFailure{}, "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := ""
	if l.storage != nil {
		path = filepath.Join(l.storage.root, "gui.log")
	}
	return append([]event{}, l.recent...), l.failuresLocked(), path, l.failed
}

// addCatalog records every fetch, including consecutive identical catalogs.
func (l *eventLog) addCatalog(c cloudhttp.Catalog) {
	if l == nil {
		return
	}
	l.diskMu.Lock()
	defer l.diskMu.Unlock()
	l.mu.Lock()
	entry := l.publishLocked(c.Message(), nil, "", c)
	l.mu.Unlock()
	l.write(entry)
	record := struct {
		Time  time.Time `json:"time"`
		Level string    `json:"level"`
		Msg   string    `json:"msg"`
		cloudhttp.Catalog
	}{time.Now().UTC(), "INFO", "device_catalog", c}
	if l.storage != nil {
		line, err := json.Marshal(record)
		failed := err != nil || l.appendLine(append(line, '\n')) != nil
		l.mu.Lock()
		l.failed = l.failed || failed
		l.mu.Unlock()
	}
}
