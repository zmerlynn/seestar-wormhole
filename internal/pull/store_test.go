package pull

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDestinationLockAndStateRefusals(t *testing.T) {
	dest := t.TempDir()
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.BindSource("test-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDestination(dest); !errors.Is(err, ErrLocked) {
		t.Fatalf("contending lock=%v", err)
	}
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	d.Close()
	reopened, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	requireReason(t, reopened.BindSource("other"), "source_mismatch")
	reopened.Close()
	filename := filepath.Join(dest, metadata, "state.json")
	for _, state := range []string{"{", `{"version":5,"source":"test-source","root":"EMMC Images/MyWorks","selections":{},"files":{}}`} {
		if err = os.WriteFile(filename, []byte(state), 0600); err != nil {
			t.Fatal(err)
		}
		if opened, err := OpenDestination(dest); err == nil {
			opened.Close()
			t.Fatal("corrupt/newer state accepted")
		} else {
			requireReason(t, err, "state_invalid")
		}
	}
}

func TestCrashPartialCleanup(t *testing.T) {
	dest := t.TempDir()
	partial := filepath.Join(dest, "a.fit.seestar-part-0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(partial, []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale partial survived: %v", err)
	}
}

func TestRegexpStateMigration(t *testing.T) {
	dest := t.TempDir()
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	d.BindSource("test-source")
	oldOnly := []string{"Dark*"}
	raw, _ := json.Marshal(oldOnly)
	oldKey := fmt.Sprintf("%x", sha256.Sum256(raw))
	d.State.Version = 1
	d.State.Selections[oldKey] = Selection{Only: oldOnly, Checkpoint: time.Now().UTC()}
	d.State.Files["Dark/a.fit"] = Receipt{Check: checkReread, Size: 1, Mtime: time.Now().UTC(), SHA256: strings.Repeat("a", 64)}
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.State.Version != stateVersion || len(d.State.Selections) != 0 || len(d.State.Files) != 1 {
		t.Fatalf("migration lost receipts or reused glob checkpoints: %+v", d.State)
	}
	if selectionKey(oldOnly) == oldKey {
		t.Fatal("regexp reused glob selection key")
	}
}

func TestSourceRebindKeepsReceiptsAndRetiresWindows(t *testing.T) {
	dest := t.TempDir()
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.BindSource("direct:PRIVATE_SERIAL_HASH")
	d.State.Files["a.fit"] = Receipt{Check: checkReread, Size: 1, Mtime: time.Now().UTC(), SHA256: strings.Repeat("a", 64)}
	d.State.Selections[selectionKey(nil)] = Selection{Checkpoint: time.Now().UTC()}
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	err = d.BindSource("via:PRIVATE_ADDRESS:4445")
	var mismatch *SourceMismatchError
	if !errors.As(err, &mismatch) || mismatch.ExistingKind != "direct" || mismatch.RequestedKind != "via" || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("unsafe/missing mismatch: %v", err)
	}
	if err = d.RebindSource("via:localhost:4445"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.State.Source != "via:localhost:4445" || len(d.State.Selections) != 0 || len(d.State.Files) != 1 {
		t.Fatalf("takeover=%+v", d.State)
	}
	err = d.BindSource("direct:PRIVATE_OTHER_HASH")
	if !errors.As(err, &mismatch) || mismatch.ExistingKind != "via" || mismatch.RequestedKind != "direct" {
		t.Fatalf("reverse mismatch=%v", err)
	}
}

func TestMalformedDirectoryStateRefusal(t *testing.T) {
	// Literal JSON keeps this independent of the state encoder and key helpers.
	// The regexp key is SHA-256 of the literal empty JSON array, [].
	for _, tc := range []struct {
		name, raw string
	}{
		{"missing dir", `{"version":5,"source":"test-source","root":"EMMC Images","selections":{"regexp:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945":{"only":null,"checkpoint":"2026-10-13T11:00:00Z"}},"files":{}}`},
		{"empty dir", `{"version":5,"source":"test-source","root":"EMMC Images","selections":{"regexp:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945":{"dir":"","only":null,"checkpoint":"2026-10-13T11:00:00Z"}},"files":{}}`},
		{"unsafe dir", `{"version":5,"source":"test-source","root":"EMMC Images","selections":{"../MyWorks/regexp:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945":{"dir":"../MyWorks","only":null,"checkpoint":"2026-10-13T11:00:00Z"}},"files":{}}`},
		{"absolute dir", `{"version":5,"source":"test-source","root":"EMMC Images","selections":{"/MyWorks/regexp:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945":{"dir":"/MyWorks","only":null,"checkpoint":"2026-10-13T11:00:00Z"}},"files":{}}`},
		{"flattened root", `{"version":5,"source":"test-source","root":"EMMC Images/MyWorks","selections":{},"files":{}}`},
		{"key disagrees with dir", `{"version":5,"source":"test-source","root":"EMMC Images","selections":{"MyWorks/regexp:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945":{"dir":"Autorun/Dark","only":null,"checkpoint":"2026-10-13T11:00:00Z"}},"files":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			dir := filepath.Join(dest, metadata)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			opened, err := OpenDestination(dest)
			if opened != nil {
				opened.Close()
			}
			requireReason(t, err, "state_invalid")
		})
	}
}
