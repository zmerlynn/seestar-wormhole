//go:build gui && guitest

package ui

import (
	"path/filepath"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
)

// A copy scenario's seeded route opens the copy tabs at launch, and its
// copy settings start copying into the shared folder.
func TestPrepareCopyScenarioOpensTheTabs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := prepareCopyScenario(root); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !routeSavedIn(settings) {
		t.Errorf("the seeded route leaves the copy tabs locked: route=%q address=%q established=%v", settings.Route, settings.LANAddress, settings.RouteEstablished)
	}
	st, err := openStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.directory.Close()
	_, set, err := loadCopySettings(st)
	if err != nil {
		t.Fatal(err)
	}
	if set == nil || set.Folder != guitest.CopyFolder(root) || !set.StartCopying {
		t.Errorf("copy settings = %+v; want copying into %q at launch", set, guitest.CopyFolder(root))
	}
}
