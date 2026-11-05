package guitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// A copy scenario's state is under its own config folder, where the
// harness's checks look too.
func TestUserConfig(t *testing.T) {
	for _, name := range []string{"HOME", "AppData", "XDG_CONFIG_HOME"} {
		t.Setenv(name, os.Getenv(name)) // restored after the test
	}
	before, beforeErr := os.UserConfigDir()
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	restore, err := UserConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := copyState()
	if err != nil || !strings.HasPrefix(got, dir+string(filepath.Separator)) {
		t.Fatalf("copy state = %q, %v; want under %q", got, err, dir)
	}
	folder := filepath.Join(root, "images")
	st, err := state.Open(got, folder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := CopyStopped(folder); err == nil {
		t.Error("CopyStopped missed the lock held under the scenario's config")
	}
	st.Close()
	if err := CopyStopped(folder); err != nil {
		t.Error(err)
	}
	restore()
	if after, err := os.UserConfigDir(); after != before || (err == nil) != (beforeErr == nil) {
		t.Errorf("restored config = %q, want %q", after, before)
	}
}
