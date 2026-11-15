package ui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
)

// startProfile starts the app on the named profile in a process of its
// own, as a launch with ui --profile does: on macOS too, the bundle's
// executable itself, which starts a new instance where open -a would
// reactivate this one. Only tests replace it.
var startProfile = func(name string) error {
	cmd, err := profileCommand(name)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// profileCommand is the process startProfile starts: this executable,
// with the name as an argument of its own and no shell, and no file of
// this process's but the standard ones.
func profileCommand(name string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "ui", "--profile", name), nil
}

// windowTitle is the window's title, which names the profile once there
// is more than one. The window takes it when it opens; a status that
// finds it changed sets it.
type windowTitle struct {
	mu   sync.Mutex
	text string
	set  func(string)
	// profiles is whether the run is of a profile, which gives the
	// window its Open Profile… menu item.
	profiles bool
}

type windowTitleKey struct{}

// titleOf is the title the window under ctx keeps; nil when the run has
// none, as the tagged GUI fixture's.
func titleOf(ctx context.Context) *windowTitle {
	t, _ := ctx.Value(windowTitleKey{}).(*windowTitle)
	return t
}

// attach makes set the way the title changes, and returns the title now.
func (t *windowTitle) attach(set func(string)) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.set = set
	return t.text
}

func (t *windowTitle) show(text string) {
	t.mu.Lock()
	set := t.set
	changed := t.text != text
	t.text = text
	t.mu.Unlock()
	if changed && set != nil {
		set(text)
	}
}

// titleFor is the window's title for a profile's label.
func titleFor(label string) string {
	if label == "" {
		return "seestar-wormhole"
	}
	return "seestar-wormhole — " + label
}

// profileView is what the page shows of the run's profile: the label the
// header and title carry, empty for Default while it is the only
// profile, and, on the page's first status after a relaunch asked for
// it, that the Profiles dialog opens.
type profileView struct {
	Label string `json:"label"`
	Ask   bool   `json:"ask,omitzero"`
}

// profileLabel is the header's and title's label for the run's profile:
// its name, or Default once a named profile exists.
func (s *server) profileLabel() string {
	if s.profile == nil {
		return ""
	}
	if s.profile.name != DefaultProfile {
		return s.profile.name
	}
	if names, _ := Profiles(s.profile.root); len(names) > 0 {
		return ProfileLabel(DefaultProfile)
	}
	return ""
}

// profileStatus is the status's profile, which only a run of a profile
// has, and sets the window's title to match. A relaunch's question is
// answered by the page's first status only, so that a poll of the page
// the relaunch reloads can't take it.
func (s *server) profileStatus(r *http.Request) *profileView {
	if s.profile == nil {
		return nil
	}
	v := &profileView{Label: s.profileLabel()}
	if s.title != nil {
		s.title.show(titleFor(v.Label))
	}
	if r.URL.Query().Has("first") {
		v.Ask = s.askProfiles.Swap(false)
	}
	return v
}

// relaunched is a relaunch's activation of this window. On Windows and
// Linux, once a named profile exists, the page asks which profile to
// open; on macOS, where a Dock click is a reopen too, it never does.
// show is an activation from another window's Profiles, which only
// brings this one forward.
func (s *server) relaunched(show bool) {
	if !show && hostOS != "darwin" && s.profile != nil {
		if names, _ := Profiles(s.profile.root); len(names) > 0 {
			s.askProfiles.Store(true)
		}
	}
	s.activate()
}

// profileEntry is a row of the Profiles dialog.
type profileEntry struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Self  bool   `json:"self"`
	Open  bool   `json:"open"`
}

// profiles lists Default and the named profiles, saying which is this
// run's and which others have an open window.
func (s *server) profiles(ctx context.Context) ([]profileEntry, error) {
	names, err := Profiles(s.profile.root)
	if err != nil {
		return nil, err
	}
	entries := make([]profileEntry, 0, len(names)+1)
	for _, name := range append([]string{DefaultProfile}, names...) {
		entries = append(entries, profileEntry{Name: name, Label: ProfileLabel(name), Self: sameProfile(name, s.profile.name)})
	}
	client := probeClient()
	defer client.CloseIdleConnections()
	var wg sync.WaitGroup
	for i := range entries {
		if entries[i].Self {
			continue
		}
		wg.Go(func() {
			_, err := probeStatus(ctx, client, ProfileDir(s.profile.root, entries[i].Name))
			entries[i].Open = err == nil
		})
	}
	wg.Wait()
	return entries, nil
}

// openProfile brings the window of the named profile forward, or starts
// the app on it when none answers.
func (s *server) openProfile(ctx context.Context, name string) error {
	dir, found, err := FindProfile(s.profile.root, name)
	if err != nil {
		return err
	}
	if sameProfile(found, s.profile.name) {
		return nil
	}
	client := probeClient()
	defer client.CloseIdleConnections()
	if activateWindow(ctx, client, dir) {
		return nil
	}
	return startProfile(found)
}

// profileInput is the body of the Profiles dialog's actions and of an
// activation.
type profileInput struct {
	Name string `json:"name"`
	Show bool   `json:"show"`
}

// serveProfiles answers the Profiles dialog: GET /api/profiles lists
// them, POST /api/profiles/open opens one and POST /api/profiles/new
// creates one and opens it. A name refused or a profile that couldn't be
// opened answers with its message, which the dialog shows as is.
func (s *server) serveProfiles(w http.ResponseWriter, r *http.Request, in profileInput) {
	refuse := func(code, message string) {
		localhttp.Respond(w, http.StatusBadRequest, struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{code, message})
	}
	if s.profile == nil {
		http.Error(w, "Request rejected", http.StatusNotFound)
		return
	}
	switch r.URL.Path {
	case "/api/profiles":
		entries, err := s.profiles(r.Context())
		if err != nil {
			refuse("profiles", "Couldn't list the profiles: "+err.Error()+".")
			return
		}
		localhttp.Respond(w, http.StatusOK, struct {
			Profiles []profileEntry `json:"profiles"`
		}{entries})
		return
	case "/api/profiles/new":
		name, err := CreateProfile(s.profile.root, in.Name)
		if refused, ok := errors.AsType[*ProfileNameError](err); ok {
			refuse("profile_name", refused.Message)
			return
		} else if err != nil {
			refuse("profile_name", "Couldn't create the profile: "+err.Error()+".")
			return
		}
		s.controller.record("Created the profile " + name + ".")
		in.Name = name
	}
	if err := s.openProfile(r.Context(), in.Name); err != nil {
		refuse("profile_open", "Couldn't open "+ProfileLabel(in.Name)+": "+err.Error()+".")
		return
	}
	localhttp.Respond(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}
