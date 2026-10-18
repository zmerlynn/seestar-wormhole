package pull

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestManifestInventoryAndStandardTool(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "Dark/a.fit", "independent literal fixture\n", now.Add(-3600000000000))
	runBatch(t, d, c, src, now)
	tool, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("GNU manifest tool unavailable")
	}
	cmd := exec.Command(tool, "-c", "SHA256SUMS")
	cmd.Dir = c.Destination
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("standard manifest validation: %v %s", err, output)
	}
	before, err := os.ReadFile(filepath.Join(c.Destination, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	// A subsequent selection has no eligible files. Its archive manifest must
	// retain the previously verified entry, including if data is moved externally.
	if err = os.Remove(filepath.Join(c.Destination, "Dark/a.fit")); err != nil {
		t.Fatal(err)
	}
	if err = d.manifest(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(c.Destination, "SHA256SUMS"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("inventory manifest lost prior receipt: %s %v", after, err)
	}
}
