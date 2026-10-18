package pull

import (
	"bytes"
	"crypto/sha256"
	jsonv1 "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Compare the actual state writer and selection hash against the v1 format.
func TestStateJSONV1Bytes(t *testing.T) {
	dest := t.TempDir()
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.State.Source = "<>&\u2028\u2029"
	d.State.Selections["z"] = Selection{Only: nil, Checkpoint: time.Date(2026, 10, 17, 0, 0, 0, 123, time.UTC)}
	d.State.Selections["a"] = Selection{Only: []string{}}
	d.State.Files["z"] = Receipt{Source: "<>&", Size: 9007199254740993, Mtime: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	for _, files := range []map[string]Receipt{d.State.Files, nil} {
		d.State.Files = files
		if err = d.save(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, metadata, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := jsonv1.MarshalIndent(d.State, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, append(want, '\n')) {
			t.Fatalf("state=%s; v1=%s", got, want)
		}
	}
	only := []string{"<>&\u2028\u2029"}
	raw, _ := jsonv1.Marshal(only)
	if got, want := legacySelectionKey(only), fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
		t.Fatalf("selection=%s; v1=%s", got, want)
	}
}
