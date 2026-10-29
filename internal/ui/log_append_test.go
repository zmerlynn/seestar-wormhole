package ui

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAppendsOpaqueHistoryWithoutRewriting(t *testing.T) {
	st := testStorage(t)
	path := filepath.Join(st.root, "gui.log")
	prior := []byte("modified history is never read into the page\n")
	if err := st.writeBytes("gui.log", prior); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	log := &eventLog{storage: st}
	log.add("Application opened.")
	recent, _, failed := log.snapshot()
	if failed || len(recent) != 1 || strings.Contains(recent[0].Message, "modified history") {
		t.Fatal("disk history affected page state")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("append replaced the existing log file")
	}
	encoded, _ := json.Marshal(recent[0])
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, prior) || len(got) != len(prior)+len(encoded)+1 {
		t.Fatal("append changed prior bytes or wrote the wrong byte count")
	}
}

func TestLogRefusesOversizedHistory(t *testing.T) {
	st := testStorage(t)
	prior := bytes.Repeat([]byte("x"), logLimit+1)
	if err := st.writeBytes("gui.log", prior); err != nil {
		t.Fatal(err)
	}
	log := &eventLog{storage: st}
	log.add("Stopped")
	if _, _, failed := log.snapshot(); !failed {
		t.Fatal("oversized prior log accepted")
	}
	got, err := os.ReadFile(filepath.Join(st.root, "gui.log"))
	if err != nil || !bytes.Equal(got, prior) {
		t.Fatal("oversized prior log modified")
	}
}
