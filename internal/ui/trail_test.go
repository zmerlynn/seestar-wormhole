package ui

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// guiLog is the records of the gui.log in st, oldest first, each as a map.
func guiLog(t *testing.T, root string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "gui.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	scan := bufio.NewScanner(bytes.NewReader(data))
	for scan.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatalf("gui.log line %q: %v", scan.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

// codes is the code of each line, in order.
func codes(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		c, _ := l["code"].(string)
		out = append(out, c)
	}
	return out
}

// Connection lines name their source, and carry neither op nor count.
func TestConnectionLinesCarrySource(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	log.add("App opened.")
	lines := guiLog(t, st.root)
	if len(lines) != 1 {
		t.Fatalf("gui.log = %v", lines)
	}
	if lines[0]["source"] != "connection" {
		t.Errorf("source = %v", lines[0]["source"])
	}
	for _, key := range []string{"op", "count"} {
		if _, ok := lines[0][key]; ok {
			t.Errorf("a connection line has %s", key)
		}
	}
	for _, key := range []string{"time", "message", "code", "phase", "reason"} {
		if _, ok := lines[0][key]; !ok {
			t.Errorf("a connection line lacks %s", key)
		}
	}
}

// A repeated connection line collapses into the last connection line,
// even with a file-tool line between them.
func TestConnectionRepeatCollapsesAcrossFileLines(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	log.add("Connected to your Seestar")
	log.addOutcome(copyapp.Outcome{Source: diagnostic.SourceBrowse, Op: diagnostic.OpConnect, Err: errors.New("synthetic"), Seq: 1})
	log.add("Connected to your Seestar")
	log.add("Stopped")
	log.add("Stopped")
	want := []string{"lifecycle", "browse_connect_failed", "lifecycle"}
	if got := codes(guiLog(t, st.root)); !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
	events, _, _ := log.snapshot()
	if len(events) != 3 {
		t.Errorf("events = %v", events)
	}
}

// A failing operation shows its latest error's text, clipped, while a
// repeat of the same class writes no new line.
func TestFailureKeepsLatestErrorText(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	open := func(path string) error {
		return &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
	}
	log.addOutcome(copyapp.Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpPass, Err: open("/Users/me/Seestar/a.fit"), Seq: 1})
	log.addOutcome(copyapp.Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpPass, Err: open("/Users/me/Seestar/" + strings.Repeat("b", 20000)), Seq: 2})
	lines := guiLog(t, st.root)
	if len(lines) != 1 || lines[0]["error"] != open("/Users/me/Seestar/a.fit").Error() {
		t.Fatalf("gui.log = %+v", lines)
	}
	_, failures, _, _ := log.trail()
	if len(failures) != 1 || failures[0].Failures != 2 || !strings.HasPrefix(failures[0].Error, "open /Users/me/Seestar/bbb") || !strings.Contains(failures[0].Error, "…(truncated, ") || len(failures[0].Error) > 2*diagnostic.MaxValue {
		t.Fatalf("failures = %+v", failures)
	}
}

// An outcome of an operation the table doesn't list writes nothing.
func TestUnknownOutcomeDropped(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	for i, o := range []copyapp.Outcome{
		{Source: diagnostic.SourceCopy, Op: "unknown", Err: errors.New("synthetic")},
		{Source: "unknown", Op: diagnostic.OpConnect, Err: errors.New("synthetic")},
		{Source: diagnostic.SourceFind, Op: diagnostic.OpList, Err: errors.New("synthetic")},
		{Source: diagnostic.SourceConnection, Op: diagnostic.OpConnect, Err: errors.New("synthetic")},
	} {
		o.Seq = uint64(i + 1)
		log.addOutcome(o)
	}
	if lines := guiLog(t, st.root); len(lines) != 0 {
		t.Errorf("gui.log = %v", lines)
	}
	if events, failures, _, _ := log.trail(); len(events) != 0 || len(failures) != 0 {
		t.Errorf("trail = %v, %v", events, failures)
	}
}

// Alternating failures and recoveries keep gui.log and its one rotation
// within the limit.
func TestTrailStaysBounded(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	for i := range 10000 {
		o := copyapp.Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpPass, Seq: uint64(i + 1)}
		if i%2 == 0 {
			o.Count = i
		}
		log.addOutcome(o)
	}
	for _, name := range []string{"gui.log", "gui.log.1"} {
		info, err := os.Stat(filepath.Join(st.root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > logLimit {
			t.Errorf("%s is %d bytes", name, info.Size())
		}
	}
	if _, _, failed := log.snapshot(); failed {
		t.Error("an append failed")
	}
}
