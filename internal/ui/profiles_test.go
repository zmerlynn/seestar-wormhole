package ui

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

// A name is refused only for what can't safely be one folder's name, and
// for default; anything else is kept as typed.
func TestCheckProfileName(t *testing.T) {
	for _, tc := range []struct{ name, message string }{
		{"", "Enter a name."},
		{strings.Repeat("x", 65), "This name is too long. Use a shorter one."},
		{"default", "Default already exists. Choose another name."},
		{"DEFAULT", "Default already exists. Choose another name."},
		{".", "A name can't start with a dot."},
		{"..", "A name can't start with a dot."},
		{".Garden", "A name can't start with a dot."},
		{"Gar/den", `A name can't contain / or \.`},
		{`Gar\den`, `A name can't contain / or \.`},
		{"Gar\x00den", "A name can't contain control characters."},
		{"Gar\nden", "A name can't contain control characters."},
		{"Gar\x7fden", "A name can't contain control characters."},
		{"Gar\u0085den", "A name can't contain control characters."},
		{"Gar\u009bden", "A name can't contain control characters."},
		{"Garden.", "A profile name can't end with a dot or a space."},
		{"Garden...", "A profile name can't end with a dot or a space."},
		{"Garden ", "A profile name can't end with a dot or a space."},
	} {
		err := CheckProfileName(tc.name)
		if refused, ok := errors.AsType[*ProfileNameError](err); !ok || refused.Message != tc.message {
			t.Errorf("%q: %v, want %q", tc.name, err, tc.message)
		}
	}
	for _, name := range []string{"Garden", "TEST0002", "Back yard S50", "Garten (Süd)", "a.b", strings.Repeat("x", 64), "défault", "De\u0301fault", "Garden\u00a0x"} {
		if err := CheckProfileName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

// A created profile is a private folder under the root's profiles folder,
// named as typed; a name differing only by case is the same profile.
func TestCreateAndFindProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if names, err := Profiles(root); err != nil || names != nil {
		t.Errorf("no root: %q, %v", names, err)
	}
	name, err := CreateProfile(root, " Garden ")
	dir := filepath.Join(root, "profiles", "Garden")
	if err != nil || name != "Garden" || ProfileDir(root, name) != dir {
		t.Fatalf("= %q, %v", name, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("no folder: %v", err)
	}
	for _, name := range []string{"Garden", "GARDEN", "garden "} {
		if _, err := CreateProfile(root, name); err == nil || err.Error() != "There's already a profile named Garden." {
			t.Errorf("%s again: %v", name, err)
		}
	}
	if _, err := CreateProfile(root, "Default"); err == nil || err.Error() != "Default already exists. Choose another name." {
		t.Errorf("created default: %v", err)
	}
	// A folder that can't be made says why, in its real words.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProfile(blocked, "Garden"); err == nil || !strings.HasPrefix(err.Error(), "Couldn't create the profile: ") || !strings.Contains(err.Error(), blocked) {
		t.Errorf("under a file: %v", err)
	}
	if _, err := CreateProfile(root, "TEST0002"); err != nil {
		t.Fatal(err)
	}
	// A file, or a folder no profile could be named, is not a profile.
	if err := os.WriteFile(filepath.Join(root, "profiles", "notes"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "profiles", ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if names, err := Profiles(root); err != nil || !slices.Equal(names, []string{"Garden", "TEST0002"}) {
		t.Errorf("Profiles = %q, %v", names, err)
	}

	for _, tc := range []struct{ name, dir, found string }{
		{"default", root, "default"},
		{"Default", root, "default"},
		{"Garden", dir, "Garden"},
		{"gARDEN", dir, "Garden"},
	} {
		if d, found, err := FindProfile(root, tc.name); err != nil || d != tc.dir || found != tc.found {
			t.Errorf("%s = %q %q, %v", tc.name, d, found, err)
		}
	}
	_, _, err = FindProfile(root, "Orchard")
	if missing, ok := errors.AsType[*NoProfileError](err); !ok || missing.Error() != "no profile named Orchard; profiles: default, Garden, TEST0002" {
		t.Errorf("Orchard: %v", err)
	}
}

// Of requests for one new name at once, one creates it; the others are
// told it exists.
func TestCreateProfileAtOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	const n = 8
	errs := make([]error, n)
	var start, wg sync.WaitGroup
	start.Add(1)
	for i := range errs {
		wg.Go(func() {
			start.Wait()
			_, errs[i] = CreateProfile(root, "Garden")
		})
	}
	start.Done()
	wg.Wait()
	created := 0
	for _, err := range errs {
		if err == nil {
			created++
		} else if err.Error() != "There's already a profile named Garden." {
			t.Errorf("= %v", err)
		}
	}
	if created != 1 {
		t.Errorf("%d created it", created)
	}
}

// Names are one profile's when they are canonically equivalent and equal
// ignoring case, as macOS and Windows folder names are: lookup, a new
// name, a run's own name and the order all go by that, and the folder
// keeps the spelling it was created with.
func TestProfileNamesCompareCanonically(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	const nfc, nfd = "Garten (S\u00fcd)", "Garten (Su\u0308d)"
	if name, err := CreateProfile(root, nfd); err != nil || name != nfd {
		t.Fatalf("= %q, %v", name, err)
	}
	for _, name := range []string{nfc, nfd, "GARTEN (S\u00dcD)", "garten (su\u0308d)"} {
		if dir, found, err := FindProfile(root, name); err != nil || found != nfd || dir != filepath.Join(root, "profiles", nfd) {
			t.Errorf("find %q = %q %q, %v", name, dir, found, err)
		}
		if _, err := CreateProfile(root, name); err == nil || err.Error() != "There's already a profile named "+nfd+"." {
			t.Errorf("create %q: %v", name, err)
		}
	}
	// ß folds to ss, which simple case folding doesn't do.
	if _, err := CreateProfile(root, "Stra\u00dfe"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := FindProfile(root, "STRASSE"); err != nil || found != "Stra\u00dfe" {
		t.Errorf("STRASSE = %q, %v", found, err)
	}
	for _, name := range []string{"alpha", "\u00c9cole"} {
		if _, err := CreateProfile(root, name); err != nil {
			t.Fatal(err)
		}
	}
	if names, err := Profiles(root); err != nil || !slices.Equal(names, []string{"alpha", nfd, "Stra\u00dfe", "\u00c9cole"}) {
		t.Errorf("Profiles = %q, %v", names, err)
	}
	if !sameProfile("DEFAULT", DefaultProfile) || !sameProfile(nfc, "garten (su\u0308d)") || sameProfile("Garten", "Garten.") {
		t.Error("sameProfile")
	}
}

// Status says which local ports the run serves, none before it arms or
// while it is copy-only, and names the holder then.
func TestStatusServingPorts(t *testing.T) {
	s := testServer(t)
	s.controller.done = make(chan struct{})
	s.controller.status = proxy.NewStatus()
	run := testSettings()
	run.Ports = []proxyrun.Port{{Device: 445, Local: 1445}, {Device: 4700, Local: 4700}}
	s.controller.runSettings = run
	status := func() (got struct {
		ServingPorts []uint16      `json:"serving_ports"`
		CopyOnly     *proxy.Holder `json:"copy_only"`
	}) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := status(); got.ServingPorts == nil || len(got.ServingPorts) != 0 || got.CopyOnly != nil {
		t.Errorf("before arming: %+v", got)
	}
	s.controller.status.MarkArmed()
	if got := status(); !slices.Contains(got.ServingPorts, 1445) || !slices.Contains(got.ServingPorts, 4700) || got.CopyOnly != nil {
		t.Errorf("armed: %+v", got)
	}
	s.controller.status = proxy.NewStatus()
	s.controller.status.MarkCopyOnly(proxy.Holder{Profile: "Garden", Telescope: "Seestar S50 TEST0002"})
	s.controller.status.MarkArmed()
	if got := status(); len(got.ServingPorts) != 0 || got.CopyOnly == nil || *got.CopyOnly != (proxy.Holder{Profile: "Garden", Telescope: "Seestar S50 TEST0002"}) {
		t.Errorf("copy-only: %+v", got)
	}
}

// ServingWindow asks every open window of the root's profiles but self,
// and only a window serving the port holds it; default shows as Default.
func TestServingWindow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if _, err := CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	answer := func(dir, body string) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/status" || r.Header.Get("X-Seestar-Token") != strings.Repeat("ab", 32) {
				http.Error(w, "Request rejected", http.StatusForbidden)
				return
			}
			io.WriteString(w, body)
		}))
		t.Cleanup(server.Close)
		st, err := openStorage(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.directory.Close()
		if err := st.write("ui-runtime.json", runtimeRecord{Port: server.Listener.Addr().(*net.TCPAddr).Port, Token: strings.Repeat("ab", 32)}); err != nil {
			t.Fatal(err)
		}
	}
	answer(root, `{"serving_ports":[1445,4700],"telescope_name":"Seestar S30 TEST0001"}`)
	answer(ProfileDir(root, "Garden"), `{"serving_ports":[],"telescope_name":"Seestar S50 TEST0002","unknown":{}}`)
	if h, ok := ServingWindow(t.Context(), root, "Garden", 1445); !ok || h != (proxy.Holder{Profile: "Default", Telescope: "Seestar S30 TEST0001"}) {
		t.Errorf("1445 from Garden = %+v, %t", h, ok)
	}
	for _, tc := range []struct {
		self string
		port uint16
	}{{"Garden", 445}, {"default", 1445}, {"DEFAULT", 4700}} {
		if h, ok := ServingWindow(t.Context(), root, tc.self, tc.port); ok {
			t.Errorf("%d from %s = %+v", tc.port, tc.self, h)
		}
	}
	if _, ok := PortHolder(t.Context(), 1445); ok {
		t.Error("a run of no profile found a holder")
	}
	if _, ok := PortHolder(WithProfile(t.Context(), root, "Garden"), 1445); !ok {
		t.Error("Garden's run found no holder")
	}
}
