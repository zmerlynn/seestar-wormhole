package pull

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPartialCleanupWarning(t *testing.T) {
	for _, name := range []string{".seestar-part-00112233445566778899aabbccddeeff", ".seestar-sums-00112233445566778899aabbccddeeff", metadata + "/.tmp-00112233445566778899aabbccddeeff", "ordinary"} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, metadata), 0700); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			d, err := OpenDestinationLogged(dir, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			_, err = os.Stat(p)
			removed := os.IsNotExist(err)
			want := name != "ordinary"
			if removed != want || (strings.Contains(logs.String(), `"rule":"stale_partial_removed"`) != want) || (want && !strings.Contains(logs.String(), `"level":"WARN"`)) || strings.Contains(logs.String(), dir) {
				t.Fatalf("removed=%v log=%s", removed, &logs)
			}
		})
	}
}
