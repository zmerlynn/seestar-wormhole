package app

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// Invented serials; the addresses are documentation addresses.
const (
	serialA = "SN-TEST-0001"
	serialB = "SN-TEST-0002"
)

var (
	keyA = SerialKey(serialA)
	keyB = SerialKey(serialB)
)

// claimedAt is the default settings with the telescopes keyed as given,
// and Folder the first one's, as a save leaves them.
func claimedAt(first string, folders map[string]string) *Settings {
	set := DefaultSettings()
	set.Folder = first
	set.Telescopes = map[string]Telescope{}
	for k, f := range folders {
		set.Telescopes[k] = Telescope{Folder: f}
	}
	return &set
}

func folderMap(set Settings) map[string]string {
	out := map[string]string{}
	for k, t := range set.Telescopes {
		out[k] = t.Folder
	}
	return out
}

func TestFolderFor(t *testing.T) {
	old := Settings{Folder: "/data/Seestar"}
	for _, key := range []string{"", keyA, AddressKey("192.0.2.10:445")} {
		if got := old.FolderFor(key); got != "/data/Seestar" {
			t.Errorf("before telescopes, %q: %q", key, got)
		}
	}
	set := *claimedAt("/data/Seestar", map[string]string{keyA: "/data/Seestar"})
	if got := set.FolderFor(keyA); got != "/data/Seestar" {
		t.Errorf("A: %q", got)
	}
	if got := set.FolderFor(keyB); got != "" {
		t.Errorf("B has %q", got)
	}
	if got := set.FolderFor(""); got != "" {
		t.Errorf("unknown has %q", got)
	}
	// An entry without a folder claims nothing.
	if got := (Settings{Folder: "/f", Telescopes: map[string]Telescope{keyA: {}}}).FolderFor(keyB); got != "/f" {
		t.Errorf("an empty entry claimed Folder: %q", got)
	}
}

func TestWithFolderAndClaim(t *testing.T) {
	old := Settings{Folder: "/data/Seestar"}
	set, ok := old.claim(keyA)
	if !ok || set.Folder != "/data/Seestar" || !maps.Equal(folderMap(set), map[string]string{keyA: "/data/Seestar"}) {
		t.Fatalf("claim = %+v, %v", set, ok)
	}
	if old.Telescopes != nil {
		t.Error("claim changed the settings it was given")
	}
	if _, ok := set.claim(keyB); ok {
		t.Error("a second telescope claimed Folder")
	}
	// The first telescope's new folder becomes Folder; another's doesn't.
	set = set.withFolder(keyB, "/data/Seestar S30 "+serialB)
	if set.Folder != "/data/Seestar" {
		t.Errorf("B's folder moved Folder: %q", set.Folder)
	}
	set = set.withFolder(keyA, "/data/A")
	if set.Folder != "/data/A" || set.FolderFor(keyB) != "/data/Seestar S30 "+serialB {
		t.Errorf("after A moved: %+v", set)
	}
	// An unknown telescope before any has a folder sets Folder alone.
	if got := (Settings{}).withFolder("", "/x"); got.Folder != "/x" || got.Telescopes != nil {
		t.Errorf("unknown: %+v", got)
	}
}

func TestAdoptMovesOneEntry(t *testing.T) {
	addr := AddressKey("192.0.2.10:445")
	set := *claimedAt("/data/A", map[string]string{keyA: "/data/A", addr: "/data/Addr"})
	got, err := set.adopt(addr, keyB)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(folderMap(got), map[string]string{keyA: "/data/A", keyB: "/data/Addr"}) || got.Folder != "/data/A" {
		t.Errorf("adopt = %+v", got)
	}
	if _, ok := set.Telescopes[keyB]; ok {
		t.Error("adopt changed the settings it was given")
	}
}

// Adopting keeps the members a later version added to either entry: the
// address's travel with its folder, and win over the serial's of the same
// name; the serial's own stay.
func TestAdoptKeepsUnknownMembers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"version":1,"folder":"/synthetic/A","telescopes":{"address:192.0.2.10:445":{"folder":"/synthetic/A","future":{"kept":true}}}}`,
			`"serial:SYNTHETIC-A":{"folder":"/synthetic/A","future":{"kept":true}}`},
		{`{"version":1,"folder":"/synthetic/A","telescopes":{"address:192.0.2.10:445":{"folder":"/synthetic/A","future":1,"from":"address"},` +
			`"serial:SYNTHETIC-A":{"later":[2],"future":0}}}`,
			`"serial:SYNTHETIC-A":{"folder":"/synthetic/A","from":"address","future":1,"later":[2]}`},
	} {
		var s Settings
		if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
			t.Fatal(err)
		}
		got, err := s.adopt(AddressKey("192.0.2.10:445"), SerialKey("SYNTHETIC-A"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "address:") {
			t.Errorf("adopt of %s = %s", tc.in, out)
		}
	}
}

func TestOverlap(t *testing.T) {
	root := abs("/data")
	a := filepath.Join(root, "A")
	set := *claimedAt(a, map[string]string{keyA: a})
	for folder, want := range map[string]string{
		a:                                   folderTaken,
		a + string(filepath.Separator):      folderTaken,
		filepath.Join(a, "sub"):             folderInside,
		root:                                folderContains,
		filepath.Join(root, "A2"):           "",
		filepath.Join(root, "B"):            "",
		filepath.Join(root, "B", "..", "A"): folderTaken,
	} {
		if got := set.overlap(keyB, folder); got != want {
			t.Errorf("%q: %q, want %q", folder, got, want)
		}
		// A telescope may keep, or move inside, its own folder.
		if got := set.overlap(keyA, folder); got != "" {
			t.Errorf("A's own %q: %q", folder, got)
		}
		if strings.Contains(want, root) {
			t.Errorf("message names the folder: %q", want)
		}
	}
	saved := foldCase
	t.Cleanup(func() { foldCase = saved })
	foldCase = true
	if got := set.overlap(keyB, strings.ToUpper(a)); got != folderTaken {
		t.Errorf("folded case: %q", got)
	}
	foldCase = false
	if got := set.overlap(keyB, strings.ToUpper(a)); got != "" {
		t.Errorf("case kept: %q", got)
	}
}

// A folder is another telescope's on disk too: through a symbolic link to
// it, to a folder above it or inside it, and with a part not made yet.
// A link that doesn't resolve fails closed.
func TestOverlapOnDisk(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(base, "images", "A")
	if err := os.MkdirAll(filepath.Join(a, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := func(name, to string) string {
		p := filepath.Join(base, name)
		if err := os.Symlink(to, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	alias := link("alias", a)
	up := link("up", filepath.Join(base, "images"))
	sub := link("sub", filepath.Join(a, "sub"))
	dangling := link("dangling", filepath.Join(base, "gone"))
	set := *claimedAt(a, map[string]string{keyA: a})
	for folder, want := range map[string]string{
		alias:                                 folderTaken,
		alias + string(filepath.Separator):    folderTaken,
		filepath.Join(alias, "new-child"):     folderInside,
		filepath.Join(alias, "new", "deeper"): folderInside,
		filepath.Join(up, "A"):                folderTaken,
		filepath.Join(up, "A", "sub"):         folderInside,
		up:                                    folderContains,
		sub:                                   folderInside,
		filepath.Join(sub, "..", "..", "B"):   "", // .. is the text's, before links
		filepath.Join(base, "images", "B"):    "",
		filepath.Join(up, "B", "new"):         "",
		dangling:                              folderUnresolved,
		filepath.Join(dangling, "child"):      folderUnresolved,
		filepath.VolumeName(base) + string(filepath.Separator): folderContains, // the root of base's drive
	} {
		if got := set.overlap(keyB, folder); got != want {
			t.Errorf("%q: %q, want %q", folder, got, want)
		}
	}
	// The other telescope's folder saved through a link is the folder.
	set = *claimedAt(alias, map[string]string{keyA: alias})
	for folder, want := range map[string]string{
		a:                         folderTaken,
		filepath.Join(a, "x"):     folderInside,
		filepath.Join(base, "B"):  "",
		filepath.Join(base, "im"): "",
	} {
		if got := set.overlap(keyB, folder); got != want {
			t.Errorf("against a link, %q: %q, want %q", folder, got, want)
		}
	}
}

// Save refuses another telescope's folder reached through a link, and a
// folder not made yet beneath one.
func TestSaveRefusesLinkedFolders(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "A")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(a, alias); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{alias, filepath.Join(alias, "new-child")} {
		e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a}))
		e.key = keyB
		w := e.post("/copy/api/settings", saveInput(folder))
		if got := decode[fieldErrors](t, w); w.Code != 400 || got.Fields["folder"] == "" || strings.Contains(got.Fields["folder"], base) {
			t.Errorf("%s: %d %+v", folder, w.Code, got)
		}
		if len(e.saves()) != 0 {
			t.Errorf("%s: saved", folder)
		}
	}
}

// A folder saved apart from another telescope's that a link changed
// later to be the same is refused as copying, a preview, Free up space or
// a clear of the history begins.
func TestLinkChangedAfterSave(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "A"), filepath.Join(base, "B")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(b, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a, keyB: link}))
	e.key = keyB
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct{ path, body string }{
		{"/copy/api/copy", `{"action":"start"}`},
		{"/copy/api/copy", `{"action":"preview"}`},
		{"/copy/api/delete", `{"action":"find","days":"0"}`},
		{"/copy/api/clear", `{}`},
	} {
		w := e.post(call.path, call.body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), errFolderShared.Error()) || strings.Contains(w.Body.String(), base) {
			t.Errorf("%s %s = %d %s", call.path, call.body, w.Code, w.Body)
		}
	}
	if e.s.copy.on() {
		t.Error("copying started into another telescope's folder")
	}
}

func TestSuggested(t *testing.T) {
	first := filepath.Join(abs("/home/u"), "Seestar")
	for name, want := range map[string]string{
		"Seestar S50 Pro " + serialB: "Seestar S50 Pro " + serialB,
		"Seestar 192.0.2.10 445":     "Seestar 192.0.2.10 445",
		`a/b\c:d*e?f"g<h>i|j`:        "a-b-c-d-e-f-g-h-i-j",
		"name. ":                     "name",
		"":                           "Seestar",
		"..":                         "Seestar",
	} {
		if got := Suggested(first, name); got != filepath.Join(abs("/home/u"), want) {
			t.Errorf("%q: %q", name, got)
		}
	}
	if got := Suggested("", "x"); got != "" {
		t.Errorf("no first folder: %q", got)
	}
}

// Entries keep the members a later version adds, and an older copy.json
// without telescopes reads as before.
func TestTelescopesJSON(t *testing.T) {
	var set Settings
	if err := json.Unmarshal([]byte(`{"version":1,"folder":"/f"}`), &set); err != nil || set.Telescopes != nil || set.Folder != "/f" {
		t.Fatalf("older file: %+v, %v", set, err)
	}
	in := `{"version":1,"folder":"/f","telescopes":{"serial:SN-TEST-0001":{"folder":"/f","later":[1,2]}}}`
	if err := json.Unmarshal([]byte(in), &set); err != nil {
		t.Fatal(err)
	}
	set = set.withFolder(keyA, "/g")
	out, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"serial:SN-TEST-0001":{"folder":"/g","later":[1,2]}`) {
		t.Errorf("saved %s", out)
	}
	out, _ = json.Marshal(Settings{Version: 1})
	if strings.Contains(string(out), "telescopes") {
		t.Errorf("no telescopes saved as %s", out)
	}
}

type telescopeStatus struct {
	SetUp    bool `json:"set_up"`
	Settings struct {
		Folder     string `json:"folder"`
		Telescopes any    `json:"telescopes"`
	} `json:"settings"`
	History   string `json:"history"`
	Telescope struct {
		ID    int  `json:"id"`
		Known bool `json:"known"`
		New   bool `json:"new"`
		Offer *struct {
			Name   string `json:"name"`
			Folder string `json:"folder"`
			Token  string `json:"token"`
		} `json:"offer"`
		Token string `json:"token"`
	} `json:"telescope"`
}

func (e *appEnv) status() telescopeStatus {
	e.t.Helper()
	return decode[telescopeStatus](e.t, e.serve("GET", "/copy/api/status", ""))
}

func (e *appEnv) set(f func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f()
}

// saveInput is goodInput with folder.
func saveInput(folder string) string {
	return strings.Replace(goodInput, quote(abs("/data/Seestar")), quote(folder), 1)
}

// A copy.json from before telescopes keeps its folder for the telescope
// saved now, on either route's key, and nothing is written until the
// folder is used.
func TestUpgradeKeepsTheFolder(t *testing.T) {
	folder := abs("/data/Seestar")
	for name, key := range map[string]string{"serial": keyA, "address": AddressKey(testEndpoint)} {
		t.Run(name, func(t *testing.T) {
			e := newAppEnv(t, savedAt(folder))
			e.key = key
			if got := e.status(); !got.SetUp || got.Settings.Folder != folder || got.Telescope.New || got.Settings.Telescopes != nil {
				t.Errorf("status = %+v", got)
			}
			if len(e.saves()) != 0 {
				t.Fatal("a status read saved the settings")
			}
			if w := e.post("/copy/api/copy", `{"action":"preview"}`); w.Code != 200 {
				t.Fatalf("preview = %d %s", w.Code, w.Body)
			}
			saves := e.saves()
			if len(saves) != 1 || saves[0].Folder != folder || !maps.Equal(folderMap(saves[0]), map[string]string{key: folder}) {
				t.Fatalf("saves = %+v", saves)
			}
			// The next telescope is new.
			e.set(func() { e.key = keyB })
			if got := e.status(); got.SetUp || !got.Telescope.New {
				t.Errorf("next telescope: %+v", got)
			}
		})
	}
	// With no saved telescope, the first one connected takes the folder.
	e := newAppEnv(t, savedAt(folder))
	e.lost = errNoRoute
	if got := e.status(); !got.SetUp || got.Settings.Folder != folder {
		t.Errorf("no route: %+v", got)
	}
	e.set(func() { e.lost, e.key = nil, keyB })
	if w := e.post("/copy/api/delete", `{"action":"find","days":"0"}`); w.Code != 200 {
		t.Fatalf("find = %d %s", w.Code, w.Body)
	}
	if saves := e.saves(); len(saves) == 0 || saves[0].FolderFor(keyB) != folder || saves[0].FolderFor(keyA) != "" {
		t.Errorf("first connected: %+v", saves)
	}
}

var errNoRoute = &noRouteError{}

type noRouteError struct{}

func (*noRouteError) Error() string { return "synthetic: no route" }

// A second telescope is asked for a folder, offered one beside the
// first's, and waits for it: Copy, Free up space and a single-file save
// wait, and Browse lists it without statuses.
func TestSecondTelescopeAskedForFolder(t *testing.T) {
	first := abs("/data/Seestar")
	e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first}))
	e.key, e.serial, e.name = keyB, serialB, "Seestar S30 "+serialB
	got := e.status()
	want := filepath.Join(abs("/data"), "Seestar S30 "+serialB)
	if got.SetUp || !got.Telescope.New || !got.Telescope.Known || got.Settings.Folder != want || got.History != "" || got.Settings.Telescopes != nil {
		t.Errorf("status = %+v", got)
	}
	for path, body := range map[string]string{"/copy/api/copy": `{"action":"preview"}`, "/copy/api/delete": `{"action":"find","days":"0"}`} {
		if w := e.post(path, body); w.Code != 409 || !strings.Contains(w.Body.String(), "this telescope has no folder yet") {
			t.Errorf("%s = %d %s", path, w.Code, w.Body)
		}
	}
	if w := e.post("/copy/api/clear", `{}`); w.Code != 409 {
		t.Errorf("clear = %d %s", w.Code, w.Body)
	}
	if list := e.serve("GET", "/browse/api/list?path=at%20192.0.2.10_445", "").Body.String(); !strings.Contains(list, `"statuses":false`) {
		t.Errorf("browse = %s", list)
	}
	if len(e.saves()) != 0 {
		t.Fatal("saved before Save")
	}
	w := e.post("/copy/api/settings", saveInput(want))
	if w.Code != 200 {
		t.Fatalf("save = %d %s", w.Code, w.Body)
	}
	saves := e.saves()
	if len(saves) != 1 || saves[0].Folder != first || !maps.Equal(folderMap(saves[0]), map[string]string{keyA: first, keyB: want}) {
		t.Errorf("saved %+v", saves)
	}
	if got := e.status(); !got.SetUp || got.Telescope.New || got.Settings.Folder != want {
		t.Errorf("after save: %+v", got)
	}
	if list := e.serve("GET", "/browse/api/list?path=at%20192.0.2.10_445", "").Body.String(); !strings.Contains(list, `"status":"new"`) {
		t.Errorf("browse after save = %s", list)
	}
	// Back at the first telescope, the field shows its folder again.
	id := e.status().Telescope.ID
	e.set(func() { e.key = keyA })
	if got := e.status(); got.Settings.Folder != first || got.Telescope.ID == id {
		t.Errorf("first again: %+v", got)
	}
}

// An address saved without a serial has its own folder.
func TestAddressTelescope(t *testing.T) {
	first := abs("/data/Seestar")
	e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first}))
	addr := AddressKey(testEndpoint)
	e.key, e.name = addr, "Seestar 192.0.2.10 445"
	want := filepath.Join(abs("/data"), "Seestar 192.0.2.10 445")
	if got := e.status(); got.SetUp || got.Settings.Folder != want {
		t.Errorf("status = %+v", got)
	}
	if w := e.post("/copy/api/settings", saveInput(want)); w.Code != 200 {
		t.Fatalf("save = %d %s", w.Code, w.Body)
	}
	if saves := e.saves(); len(saves) != 1 || saves[0].FolderFor(addr) != want || saves[0].FolderFor(keyA) != first {
		t.Errorf("saved %+v", saves)
	}
}

// Save refuses another telescope's folder, one inside it and one holding
// it, without naming either; and a telescope not known yet can't take a
// folder once another has one.
func TestSaveRefusesSharedFolders(t *testing.T) {
	first := abs("/data/Seestar")
	for name, folder := range map[string]string{
		"same":     first,
		"inside":   filepath.Join(first, "B"),
		"contains": abs("/data"),
	} {
		e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first}))
		e.key = keyB
		w := e.post("/copy/api/settings", saveInput(folder))
		got := decode[fieldErrors](t, w)
		if w.Code != 400 || got.Fields["folder"] == "" || strings.Contains(got.Fields["folder"], abs("/data")) {
			t.Errorf("%s: %d %+v", name, w.Code, got)
		}
		if len(e.saves()) != 0 {
			t.Errorf("%s: saved", name)
		}
	}
	e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first}))
	w := e.post("/copy/api/settings", saveInput(abs("/data/Other")))
	if got := decode[fieldErrors](t, w); w.Code != 400 || !strings.Contains(got.Fields["folder"], "Connect to the telescope first") {
		t.Errorf("unknown telescope: %d %+v", w.Code, got)
	}
	// The first telescope may save its own folder again.
	e.set(func() { e.key = keyA })
	if w := e.post("/copy/api/settings", saveInput(first)); w.Code != 200 {
		t.Errorf("own folder: %d %s", w.Code, w.Body)
	}
}

// When a telescope saved with its serial is at an address that has its own
// folder, Copy offers that folder, and only an accept moves it.
func TestAddressFolderOffered(t *testing.T) {
	first, addrFolder := abs("/data/Seestar"), abs("/data/Seestar 192.0.2.10 445")
	addr := AddressKey(testEndpoint)
	set := claimedAt(first, map[string]string{keyA: first, addr: addrFolder})
	entry := set.Telescopes[addr]
	entry.Unknown = jsontext.Value(`{"future":true}`)
	set.Telescopes[addr] = entry
	e := newAppEnv(t, set)
	e.key, e.serial, e.name = keyB, serialB, "Seestar S30 "+serialB
	got := e.status()
	if got.Telescope.Offer == nil || got.Telescope.Offer.Name != "Seestar S30 "+serialB || got.Telescope.Offer.Folder != addrFolder || got.SetUp {
		t.Fatalf("status = %+v", got)
	}
	// Nothing is reassigned by looking, nor by using Copy.
	e.post("/copy/api/copy", `{"action":"preview"}`)
	if len(e.saves()) != 0 {
		t.Fatalf("reassigned silently: %+v", e.saves())
	}
	w := e.post("/copy/api/adopt", `{}`)
	if w.Code != 200 {
		t.Fatalf("adopt = %d %s", w.Code, w.Body)
	}
	saves := e.saves()
	if len(saves) != 1 || !maps.Equal(folderMap(saves[0]), map[string]string{keyA: first, keyB: addrFolder}) ||
		string(saves[0].Telescopes[keyB].Unknown) != `{"future":true}` {
		t.Errorf("saved %+v", saves)
	}
	if got := e.status(); !got.SetUp || got.Telescope.Offer != nil || got.Settings.Folder != addrFolder {
		t.Errorf("after adopt: %+v", got)
	}
	if w := e.post("/copy/api/adopt", `{}`); w.Code != 409 {
		t.Errorf("second adopt = %d", w.Code)
	}
	// Through the account, the address isn't where the telescope is.
	e = newAppEnv(t, claimedAt(first, map[string]string{keyA: first, addr: addrFolder}))
	e.key, e.serial, e.device, e.run = keyB, serialB, "Seestar S30 "+serialB, 1
	if got := e.status(); got.Telescope.Offer != nil {
		t.Errorf("account route offered %+v", got.Telescope.Offer)
	}
}

// A Free up space preview stays at its telescope: another telescope at the
// same address can't delete from it.
func TestFreeBoundToItsTelescope(t *testing.T) {
	e := newChangeEnv(t)
	e.route.Lock()
	e.key = keyA
	e.route.Unlock()
	v := e.preview()
	e.route.Lock()
	e.key = keyB
	e.route.Unlock()
	if w := e.delete(v); w.Code != 409 || !strings.Contains(w.Body.String(), "Preview again") {
		t.Errorf("delete at another telescope = %d %s", w.Code, w.Body)
	}
	if !e.has("MyWorks/a.jpg") {
		t.Error("a file was deleted")
	}
	// The preview gave A the folder: B has none.
	if w := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); w.Code != 409 || !strings.Contains(w.Body.String(), "no folder yet") {
		t.Errorf("find at B = %d %s", w.Code, w.Body)
	}
}

// A single-file save waits for the telescope's folder, without a dialog;
// once it has one, a save into another telescope's folder still holds
// that folder's lock.
func TestSaveFileWaitsForFolder(t *testing.T) {
	first := t.TempDir()
	e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first}))
	e.key = keyB
	f := &fakeNative{answer: filepath.Join(first, "x.txt")}
	e.s.native = f.native()
	if w := e.serve("GET", xURL, ""); w.Code != http.StatusNoContent {
		t.Fatalf("save = %d", w.Code)
	}
	if len(f.saves) != 0 || !slices.Equal(f.alerts, []string{noFolderAlert}) {
		t.Errorf("dialogs %q, alerts %q", f.saves, f.alerts)
	}
	if _, err := os.Stat(f.answer); err == nil {
		t.Error("saved before the folder was chosen")
	}
	if w := e.post("/copy/api/settings", saveInput(t.TempDir())); w.Code != 200 {
		t.Fatalf("settings = %d %s", w.Code, w.Body)
	}
	root, err := os.OpenRoot(first)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	held, err := state.LockFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	f.alerts = nil
	e.serve("GET", xURL, "")
	if !slices.Equal(f.alerts, []string{otherFolderAlert}) {
		t.Errorf("save into the first telescope's folder: alerts %q", f.alerts)
	}
}

const otherFolderAlert = "That folder is another telescope's. Save the file outside it."

// A telescope's file is never saved into another telescope's folder, free
// or held, nor through a link to it; its own folder takes the lock.
func TestSaveFileOutsideOtherFolders(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a, keyB: b}))
	e.key = keyB
	if err := os.Mkdir(filepath.Join(a, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(a, "a.txt")
	if err := os.WriteFile(theirs, []byte("from A"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	f := &fakeNative{}
	e.s.native = f.native()
	for _, dst := range []string{theirs, filepath.Join(a, "sub", "new.txt"), filepath.Join(link, "a.txt"), filepath.Join(link, "sub", "x.txt")} {
		f.answer, f.alerts = dst, nil
		e.serve("GET", xURL, "")
		if !slices.Equal(f.alerts, []string{otherFolderAlert}) || strings.Contains(e.logs.String(), a) {
			t.Errorf("%s: alerts %q", dst, f.alerts)
		}
	}
	if got, _ := os.ReadFile(theirs); string(got) != "from A" {
		t.Errorf("A's file is %q", got)
	}
	if _, err := os.Stat(filepath.Join(a, "sub", "new.txt")); err == nil {
		t.Error("saved into A's folder")
	}
	// Its own folder, and anywhere else, it may.
	for _, dst := range []string{filepath.Join(b, "x.txt"), filepath.Join(t.TempDir(), "x.txt")} {
		f.answer, f.alerts = dst, nil
		e.serve("GET", xURL, "")
		if got, _ := os.ReadFile(dst); len(f.alerts) != 0 || string(got) != "x" {
			t.Errorf("%s: alerts %q, saved %q", dst, f.alerts, got)
		}
	}
}

// A save whose telescope, or its folder, changes while the dialog is open
// is refused: the file is the telescope's the dialog was opened for.
func TestSaveFileAfterChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(e *appEnv, b string)
	}{
		{"folder saved", func(e *appEnv, b string) {
			// Opening Browse again retires the dialog's pool, so the
			// folder's change leaves its session open.
			if w := e.serve("GET", "/browse/", ""); w.Code != 200 {
				e.t.Errorf("browse = %d", w.Code)
			}
			if w := e.post("/copy/api/settings", saveInput(filepath.Join(b, "moved"))); w.Code != 200 {
				e.t.Errorf("settings = %d %s", w.Code, w.Body)
			}
		}},
		{"route changed", func(e *appEnv, _ string) {
			if err := e.m.Change(func() bool { return true }, func() error {
				e.set(func() { e.key, e.endpoint = keyA, atB })
				return nil
			}); err != nil {
				e.t.Error(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := t.TempDir(), t.TempDir()
			e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a, keyB: b}))
			e.key = keyB
			dst := filepath.Join(t.TempDir(), "x.txt")
			entered, release := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			var alerts []string
			e.s.native = Native{
				SaveFile: func(string) (string, error) { close(entered); <-release; return dst, nil },
				Alert: func(m string) {
					mu.Lock()
					defer mu.Unlock()
					alerts = append(alerts, m)
				},
			}
			saved := make(chan struct{})
			go func() { defer close(saved); e.serve("GET", xURL, "") }()
			<-entered
			tc.change(e, b)
			close(release)
			<-saved
			if _, err := os.Stat(dst); err == nil {
				t.Error("saved after the change")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(alerts) != 1 {
				t.Errorf("alerts %q", alerts)
			}
		})
	}
}

// Saving a file waits for its telescope's folder, the first telescope's
// too, in the window and in the browser: no dialog, no file and no
// download until Copy has one. Listing and viewing need none.
func TestDownloadWaitsForFolder(t *testing.T) {
	first := t.TempDir()
	for _, tc := range []struct {
		name string
		set  *Settings
		key  string
	}{
		{"first telescope, nothing saved", nil, keyA},
		{"first telescope, no folder saved", &Settings{Version: 1, IntervalMinutes: 10, FilesAtOnce: 4}, keyA},
		{"second telescope", claimedAt(first, map[string]string{keyA: first}), keyB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAppEnv(t, tc.set)
			e.key = tc.key
			w := e.serve("GET", xURL, "")
			if w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != noFolderAlert {
				t.Errorf("browser download = %d %q", w.Code, w.Body)
			}
			if w := e.serve("GET", strings.TrimSuffix(xURL, "&download=1"), ""); w.Code != 200 || w.Body.String() != "x" {
				t.Errorf("view = %d %q", w.Code, w.Body)
			}
			f := &fakeNative{answer: filepath.Join(t.TempDir(), "x.txt")}
			e.s.native = f.native()
			if w := e.serve("GET", xURL, ""); w.Code != http.StatusNoContent || len(f.saves) != 0 || !slices.Equal(f.alerts, []string{noFolderAlert}) {
				t.Errorf("save = %d, dialogs %q, alerts %q", w.Code, f.saves, f.alerts)
			}
			if _, err := os.Stat(f.answer); err == nil {
				t.Error("saved before the folder was chosen")
			}
		})
	}
}

// No log or Activity line names a telescope's folder, whatever Copy,
// Test, Save, the offer and Free up space do; they name its serial and
// its address.
func TestTelescopeFoldersStayOutOfLogs(t *testing.T) {
	const serial = "SN-SENTINEL-9"
	first := filepath.Join(t.TempDir(), "FOLDER-SENTINEL-A")
	second := filepath.Join(t.TempDir(), "FOLDER-SENTINEL-B")
	addrFolder := filepath.Join(t.TempDir(), "FOLDER-SENTINEL-ADDR")
	share := t.TempDir()
	if err := os.MkdirAll(filepath.Join(share, "MyWorks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, "MyWorks", "a.jpg"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first, AddressKey(testEndpoint): addrFolder}))
	e.d.dir = share
	e.key, e.serial, e.name = SerialKey(serial), serial, "Seestar S30 "+serial
	e.status()
	e.post("/copy/api/copy", `{"action":"preview"}`)
	e.post("/copy/api/settings", saveInput(first))
	e.post("/copy/api/adopt", `{}`)
	e.post("/copy/api/test", `{"address":"192.0.2.10"}`)
	e.post("/copy/api/settings", saveInput(second))
	e.post("/copy/api/clear", `{}`)
	// Start logs "copying started" before it answers.
	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	e.post("/copy/api/copy", `{"action":"stop"}`)
	e.post("/copy/api/delete", `{"action":"find","days":"0"}`)
	lines, _ := e.s.copy.activity.lines()
	var activity strings.Builder
	for _, l := range lines {
		activity.WriteString(l.Text + "\n")
	}
	if !strings.Contains(activity.String(), "copying started") {
		t.Errorf("Activity lacks the start: %s", activity.String())
	}
	for where, text := range map[string]string{"log": e.logs.String(), "Activity": activity.String()} {
		if strings.Contains(text, "FOLDER-SENTINEL") {
			t.Errorf("%s holds a folder:\n%s", where, text)
		}
		for _, s := range []string{serial, "192.0.2.10:445"} {
			if !strings.Contains(text, s) {
				t.Errorf("%s lacks %q:\n%s", where, s, text)
			}
		}
	}
}

// withoutFolder drops the private attributes, in a group too, and keeps
// the rest.
func TestWithoutFolder(t *testing.T) {
	var out lockedBuffer
	log := slog.New(withoutFolder{slog.NewTextHandler(&out, nil)}).With("dest", "/w1", "kept", 1)
	log.WithGroup("g").Info("m", "to", "/w2", "endpoint", "192.0.2.10:445", "n", 2)
	log.Info("m", slog.Group("s", "dest", "/w3", "n", 3))
	got := out.String()
	for _, s := range []string{"/w1", "/w2", "/w3"} {
		if strings.Contains(got, s) {
			t.Errorf("log holds %q: %s", s, got)
		}
	}
	for _, s := range []string{"kept=1", "g.endpoint=192.0.2.10:445", "g.n=2", "s.n=3"} {
		if !strings.Contains(got, s) {
			t.Errorf("log lacks %q: %s", s, got)
		}
	}
}

// Copying started for one telescope ends before any dial, and writes no
// history, once the target at the same address is another telescope's:
// its folder isn't the one copying writes to.
func TestCopyEndsAtAnotherKey(t *testing.T) {
	a := t.TempDir()
	e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a}))
	e.key = keyA
	target := e.s.copy.target
	e.s.copy.target = func() (Target, error) {
		t, err := target()
		t.Telescope = keyB
		return t, err
	}
	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	e.s.copy.mu.Lock()
	done := e.s.copy.done
	e.s.copy.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("copying followed another telescope")
		}
	}
	e.d.mu.Lock()
	dialed := len(e.d.endpoints)
	e.d.mu.Unlock()
	if dialed != 0 {
		t.Errorf("dialed %d times for another telescope", dialed)
	}
	// The pass holds the folder's lock before it follows the target.
	if entries, err := os.ReadDir(a); err != nil || len(entries) > 1 || len(entries) == 1 && entries[0].Name() != plan.LockName {
		t.Errorf("the folder holds %v, %v", entries, err)
	}
	if v := e.s.copy.view(time.Now()); v.On {
		t.Errorf("copying = %+v", v)
	}
}

// The controller's own check: a pass for a telescope bound at one key
// refuses, and ends copying, at another key at the same address.
func TestFollowRefusesAnotherKey(t *testing.T) {
	e := newAppEnv(t, claimedAt(t.TempDir(), map[string]string{}))
	e.key = keyB
	e.s.copy.bound = Target{Endpoint: testEndpoint, Telescope: keyA}
	canceled := false
	e.s.copy.cancel = func() { canceled = true }
	if _, err := e.s.copy.follow(testEndpoint); !errors.Is(err, errOtherTelescope) || !canceled {
		t.Errorf("follow = %v, canceled %v", err, canceled)
	}
}

// A route change, copying, a preview, Free up space and a clear of the
// history are all refused, and nothing moves, while the first telescope's
// folder can't be saved as its own; and so still after a restart.
func TestClaimFailureRefuses(t *testing.T) {
	a := t.TempDir()
	saved := savedAt(a)
	for _, restart := range []bool{false, true} {
		e := newAppEnv(t, saved)
		e.key = keyA
		e.saveErr = errors.New("synthetic settings write failure")
		changed := false
		err := e.m.Change(func() bool { return true }, func() error {
			e.set(func() { e.key = keyB })
			changed = true
			return nil
		})
		if !errors.Is(err, ErrClaim) || changed {
			t.Fatalf("restart %v: Change = %v, committed %v", restart, err, changed)
		}
		for _, req := range [][2]string{
			{"/copy/api/copy", `{"action":"start"}`},
			{"/copy/api/copy", `{"action":"preview"}`},
			{"/copy/api/delete", `{"action":"find","days":"0"}`},
			{"/copy/api/clear", `{}`},
		} {
			w := e.post(req[0], req[1])
			if w.Code == 200 || !strings.Contains(w.Body.String(), ErrClaim.Error()) {
				t.Errorf("restart %v: %s %s = %d %s", restart, req[0], req[1], w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), a) {
				t.Errorf("restart %v: %s answer holds the folder", restart, req[0])
			}
		}
		if e.s.copy.on() || e.s.preview.running() || e.s.free.running() {
			t.Errorf("restart %v: something started", restart)
		}
		// A keeps the folder; the settings still have it as every
		// telescope's.
		if !e.status().SetUp {
			t.Errorf("restart %v: A lost its folder", restart)
		}
		e.s.mu.Lock()
		claimed := e.s.file.settings.claimed()
		e.s.mu.Unlock()
		if claimed || len(e.saves()) != 0 {
			t.Errorf("restart %v: claimed %v, saves %d", restart, claimed, len(e.saves()))
		}
		if strings.Contains(e.logs.String(), a) {
			t.Errorf("restart %v: log holds the folder: %s", restart, e.logs.String())
		}
		e.m.Quit()
	}
}

// An adoption is of the offer the page showed: after the telescope at the
// page's address changes to one with an offer of its own, the old page's
// click, with its token or with none, adopts nothing.
func TestStaleAdoptRefused(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	e := newAppEnv(t, claimedAt(a, map[string]string{AddressKey(atA): a, AddressKey(atB): b}))
	e.key, e.serial, e.endpoint = keyA, serialA, atA
	shown := e.status()
	if shown.Telescope.Offer == nil || shown.Telescope.Offer.Folder != a || shown.Telescope.Offer.Token == "" {
		t.Fatalf("no offer of A's address folder: %+v", shown.Telescope)
	}
	for _, tok := range []string{shown.Telescope.Offer.Token, shown.Telescope.Token} {
		for _, s := range []string{serialA, "192.0.2.", a} {
			if strings.Contains(tok, s) {
				t.Errorf("token %q names %q", tok, s)
			}
		}
	}
	if err := e.m.Change(func() bool { return true }, func() error {
		e.set(func() { e.key, e.serial, e.endpoint = keyB, serialB, atB })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"token":` + quote(shown.Telescope.Offer.Token) + `}`} {
		if w := e.serve("POST", "/copy/api/adopt", body, "Content-Type", "application/json"); w.Code != http.StatusConflict {
			t.Errorf("adopt %s = %d %s", body, w.Code, w.Body)
		}
	}
	if len(e.saves()) != 0 {
		t.Error("a stale acceptance changed which telescope has which folder")
	}
	// B's own offer, as status shows it now, is adopted.
	if w := e.post("/copy/api/adopt", `{}`); w.Code != 200 {
		t.Fatalf("adopt B's offer = %d %s", w.Code, w.Body)
	}
	if saves := e.saves(); len(saves) != 1 || saves[0].FolderFor(keyB) != b || saves[0].FolderFor(keyA) != "" {
		t.Errorf("saves = %+v", saves)
	}
}

// A Save is of the telescope whose folder the page showed: a form filled
// for A doesn't become B's folder once the target is B.
func TestStaleSaveRefused(t *testing.T) {
	a := t.TempDir()
	e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a}))
	e.key = keyA
	token := e.status().Telescope.Token
	e.set(func() { e.key = keyB })
	folder := filepath.Join(t.TempDir(), "B")
	body := `{"token":` + quote(token) + "," + saveInput(folder)[1:]
	if w := e.serve("POST", "/copy/api/settings", body, "Content-Type", "application/json"); w.Code != http.StatusConflict {
		t.Errorf("stale save = %d %s", w.Code, w.Body)
	}
	if w := e.serve("POST", "/copy/api/settings", saveInput(folder), "Content-Type", "application/json"); w.Code != http.StatusConflict {
		t.Errorf("save without a token = %d %s", w.Code, w.Body)
	}
	if len(e.saves()) != 0 {
		t.Error("a stale Save changed the settings")
	}
	if w := e.post("/copy/api/settings", saveInput(folder)); w.Code != 200 {
		t.Errorf("B's save = %d %s", w.Code, w.Body)
	}
}
