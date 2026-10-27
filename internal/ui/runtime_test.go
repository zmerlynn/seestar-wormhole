package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

func TestHeldLockDoesNotReplaceStaleRecord(t *testing.T) {
	st := testStorage(t)
	if err := st.write("ui-runtime.json", struct {
		Stale bool `json:"stale"`
	}{true}); err != nil {
		t.Fatal(err)
	}
	lock, err := privatefile.Open(filepath.Join(st.root, "ui.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	acquired, err := tryLock(lock)
	if err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runUI(ctx, st.root, io.Discard, nil, func(string) error { t.Error("opened stale record"); return nil }, &fakeKeychain{}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Stale bool `json:"stale"`
	}
	if err := st.read("ui-runtime.json", &got); err != nil || !got.Stale {
		t.Fatal("record changed without lock", err)
	}
}
func TestOpenFailurePrintsPrivateURL(t *testing.T) {
	var out strings.Builder
	openPage("http://127.0.0.1:12345/#synthetic-control-token", func(string) error { return errStorage }, &out)
	if !strings.Contains(out.String(), "/#synthetic-control-token") {
		t.Fatal("missing manual URL")
	}
}
func TestRuntimeRecordBounds(t *testing.T) {
	for _, r := range []runtimeRecord{{0, strings.Repeat("a", 64)}, {65536, strings.Repeat("a", 64)}, {12345, ""}, {12345, strings.Repeat("z", 64)}} {
		if r.valid() {
			t.Fatal("invalid runtime record accepted")
		}
	}
}
