package app

import (
	"encoding/json/v2"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	got := set.adopt(addr, keyB)
	if !maps.Equal(folderMap(got), map[string]string{keyA: "/data/A", keyB: "/data/Addr"}) || got.Folder != "/data/A" {
		t.Errorf("adopt = %+v", got)
	}
	if _, ok := set.Telescopes[keyB]; ok {
		t.Error("adopt changed the settings it was given")
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
		} `json:"offer"`
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
	e := newAppEnv(t, claimedAt(first, map[string]string{keyA: first, addr: addrFolder}))
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
	if len(saves) != 1 || !maps.Equal(folderMap(saves[0]), map[string]string{keyA: first, keyB: addrFolder}) {
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
	if !slices.Equal(f.alerts, []string{"Another copy or delete is using this folder. Try saving it again."}) {
		t.Errorf("save into the first telescope's folder: alerts %q", f.alerts)
	}
}

// No log or Activity line names a telescope's folder, its serial or its
// address, whatever Copy, Test, Save, the offer and Free up space do.
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
	e.post("/copy/api/copy", `{"action":"start"}`)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(e.logs.String(), "copying started") {
		if time.Now().After(deadline) {
			t.Fatalf("copying never started: %s", e.logs.String())
		}
		time.Sleep(5 * time.Millisecond)
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
		for _, s := range []string{"FOLDER-SENTINEL", serial, "192.0.2.10", "SN-TEST"} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q:\n%s", where, s, text)
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
	for _, s := range []string{"/w1", "/w2", "/w3", "192.0.2.10"} {
		if strings.Contains(got, s) {
			t.Errorf("log holds %q: %s", s, got)
		}
	}
	for _, s := range []string{"kept=1", "g.n=2", "s.n=3"} {
		if !strings.Contains(got, s) {
			t.Errorf("log lacks %q: %s", s, got)
		}
	}
}
