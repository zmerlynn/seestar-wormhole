package ui

import (
	"encoding/json/v2"
	"path/filepath"
	"testing"
)

// copyPageStatuses is the server's own status and copy status before and
// after a LAN save and a copy settings save, and the settings save's reply.
func copyPageStatuses(t *testing.T) string {
	t.Helper()
	s, _ := copyServer(t)
	locked := serve(s, request(s, "GET", "/api/status", "")).Body.String()
	before := serve(s, request(s, "GET", "/copy/api/status", "")).Body.String()
	if w := serve(s, request(s, "POST", "/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.20"}}`)); w.Code != 200 {
		t.Fatalf("save lan = %d %s", w.Code, w.Body)
	}
	unlocked := serve(s, request(s, "GET", "/api/status", "")).Body.String()
	folder, _ := json.Marshal(filepath.Join(t.TempDir(), "Seestar"))
	body := `{"folder":` + string(folder) + `,"interval":"5","files_at_once":"4","excludes":["Video"],"includes":[],"skip_built_in":true,"start_copying":false}`
	w := serve(s, request(s, "POST", "/copy/api/settings", body))
	if w.Code != 200 {
		t.Fatalf("copy settings = %d %s", w.Code, w.Body)
	}
	after := serve(s, request(s, "GET", "/copy/api/status", "")).Body.String()
	return `{locked:` + locked + `,unlocked:` + unlocked + `,before:` + before + `,saved:` + w.Body.String() + `,after:` + after + `}`
}

// The copy tabs stay disabled until a route is saved, and the reach notice
// links to what it names.
func TestCopyUnlockPage(t *testing.T) {
	runPageRegression(t, "copy-page-test.js", "checkCopyUnlock("+copyPageStatuses(t)+")")
}

// The first visit has the settings form open with Save and continue; after
// it, a summary line and Change….
func TestCopySetupPage(t *testing.T) {
	runPageRegression(t, "copy-page-test.js", "checkCopySetup("+copyPageStatuses(t)+")")
}

// Activity merges the connection's events and copy's activity, newest
// first, each tagged; the diagnostic holds both settings and the log path.
func TestActivityMergePage(t *testing.T) {
	runPageRegression(t, "copy-page-test.js", "checkActivityMerge("+copyPageStatuses(t)+")")
}

// Free up space's preview counts the checked folders and deletes only them.
func TestFreePreviewPage(t *testing.T) {
	runPageRegression(t, "copy-page-test.js", "checkFreePreview("+copyPageStatuses(t)+")")
}

// Choose… is the window's: it fills the folder, and a failure says to type
// it.
func TestCopyChoosePage(t *testing.T) {
	runPageRegression(t, "copy-page-test.js", "checkCopyChoose("+copyPageStatuses(t)+")")
}

// Browse before the folder is saved says where to set it.
func TestBrowseSetupPage(t *testing.T) {
	runPageRegression(t, "copy-page-test.js", "checkBrowseSetup("+copyPageStatuses(t)+")")
}
