package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// useProfileRoot makes root the default profile's folder for the test.
func useProfileRoot(t *testing.T, root string, err error) {
	old := profileRoot
	t.Cleanup(func() { profileRoot = old })
	profileRoot = func() (string, error) { return root, err }
}

// writeSavedCopy writes copy.json as the app creates it.
func writeSavedCopy(t *testing.T, root, body string) {
	t.Helper()
	f, err := privatefile.Open(filepath.Join(root, "copy.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(body); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

// profileFiles is each file under root with its contents and time.
func profileFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		info, ierr := d.Info()
		if err = errors.Join(err, ierr); err != nil {
			return err
		}
		got[p] = string(b) + "@" + info.ModTime().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// fullCopyJSON is a copy.json with every member set, and its history
// folder: the Copy members fill what they name, Free up space's never. The
// history folder is absolute on this system, as the loader requires; on
// Windows that needs a drive.
func fullCopyJSON(t *testing.T) (body, history string) {
	t.Helper()
	history = filepath.Join(t.TempDir(), "history")
	quoted, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	return `{"version":1,"folder":"/synthetic/Seestar","history_dir":` + string(quoted) + `,"interval_minutes":9,
"excludes":["copy-exclude"],"includes":["copy-include"],"skip_built_in":false,"start_copying":true,"files_at_once":3,"once":true,
"older_than_days":1,"trust_state":true,"free_excludes":["free-exclude"],"free_includes":["free-include"]}`, history
}

// The default profile is copy.json's Copy members and config.json's
// endpoint, read without a write.
func TestDefaultProfileLoad(t *testing.T) {
	root := savedConfigRoot(t)
	body, history := fullCopyJSON(t)
	writeSavedCopy(t, root, body)
	useProfileRoot(t, root, nil)
	before := profileFiles(t, root)
	got, err := loadProfile("default")
	want := cli.Saved{
		Dest: "/synthetic/Seestar", HistoryDir: history, Endpoint: "127.0.0.2:1445",
		Excludes: []string{"copy-exclude"}, Includes: []string{"copy-include"}, NoDefaultExcludes: true,
		Interval: 9 * time.Minute, Once: true, Workers: 3,
	}
	// The saved endpoint is through the account, so it comes with its
	// check.
	if got.CheckEndpoint == nil {
		t.Error("no check of the account route's endpoint")
	}
	got.CheckEndpoint = nil
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("= %+v, %v; want %+v", got, err, want)
	}
	if after := profileFiles(t, root); !maps.Equal(after, before) {
		t.Errorf("the state folder changed: %v, was %v", after, before)
	}

	// The skip-built-in default and an absent history fill neither flag.
	if err := os.Remove(filepath.Join(root, "copy.json")); err != nil {
		t.Fatal(err)
	}
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/Seestar"}`)
	got, err = loadProfile("default")
	if err != nil || got.HistoryDir != "" || got.NoDefaultExcludes || got.Dest != "/synthetic/Seestar" || got.Workers != 4 || got.Interval != 5*time.Minute {
		t.Errorf("a short copy.json = %+v, %v", got, err)
	}

	// Without a usable config.json, only the endpoint is left unfilled.
	writeSavedConfig(t, root, []byte(`{"version":99}`))
	if got, err = loadProfile("default"); err != nil || got.Endpoint != "" || got.Dest == "" {
		t.Errorf("a refused config.json = %+v, %v", got, err)
	}
	if err := os.Remove(filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if got, err = loadProfile("default"); err != nil || got.Endpoint != "" || got.Dest == "" {
		t.Errorf("no config.json = %+v, %v", got, err)
	}
	code, record, _ := usageExit(t, "sync", "--profile", "default")
	if code != 2 || record.Option != "--endpoint" || record.Rule != "required; give the SMB host:port" {
		t.Errorf("sync without a saved endpoint: exit %d %+v", code, record)
	}
}

// A named profile is read from its own folder under the root, matched
// without regard to case, and the root's own files aren't its. A name
// with no folder is exit 2 naming the profiles there are.
func TestNamedProfileLoad(t *testing.T) {
	root := savedConfigRoot(t)
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/default"}`)
	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	writeSavedCopy(t, ui.ProfileDir(root, "Garden"), `{"version":1,"folder":"/synthetic/Garden"}`)
	useProfileRoot(t, root, nil)
	for _, name := range []string{"Garden", "garden"} {
		if got, err := loadProfile(name); err != nil || got.Dest != "/synthetic/Garden" || got.Endpoint != "" {
			t.Errorf("%s = %+v, %v", name, got, err)
		}
	}
	if got, err := loadProfile("default"); err != nil || got.Dest != "/synthetic/default" {
		t.Errorf("default = %+v, %v", got, err)
	}
	for _, cmd := range copyCommandNames {
		code, record, _ := usageExit(t, cmd, "--profile", "TEST0002")
		if code != 2 || record.Option != "--profile" || record.Rule != "no profile named TEST0002; profiles: default, Garden" {
			t.Errorf("%s: exit %d %+v", cmd, code, record)
		}
	}
}

// Each refusal is exit 2 for --profile with its rule and no path, and
// writes nothing.
func TestDefaultProfileRefusals(t *testing.T) {
	root := savedConfigRoot(t)
	for _, tc := range []struct {
		name, copyJSON string
		root           string
		rootErr        error
		rule           string
	}{
		{"no config folder", "", "", errors.New("no config folder"), "no per-user config folder for the app's profiles; give the flags"},
		{"no state folder", "", filepath.Join(t.TempDir(), "missing"), nil, "no saved Copy settings; save them on the app's Copy tab, or give the flags"},
		{"no copy.json", "", root, nil, "no saved Copy settings; save them on the app's Copy tab, or give the flags"},
		{"another version", `{"version":2,"folder":"/synthetic/Seestar"}`, root, nil, "use a valid saved copy.json in the app's state folder"},
		{"not JSON", `{`, root, nil, "use a valid saved copy.json in the app's state folder"},
		{"relative history", `{"version":1,"history_dir":"synthetic/history"}`, root, nil, "use a valid saved copy.json in the app's state folder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(filepath.Join(root, "copy.json"))
			if tc.copyJSON != "" {
				writeSavedCopy(t, root, tc.copyJSON)
			}
			useProfileRoot(t, tc.root, tc.rootErr)
			before := profileFiles(t, root)
			for _, cmd := range copyCommandNames {
				code, record, logs := usageExit(t, cmd, "--profile", "default")
				if code != 2 || record.Option != "--profile" || record.Rule != tc.rule || strings.Contains(logs, root) || strings.Contains(logs, "synthetic") {
					t.Errorf("%s: exit %d %+v\n%s", cmd, code, record, logs)
				}
			}
			if after := profileFiles(t, root); !maps.Equal(after, before) {
				t.Errorf("the state folder changed: %v, was %v", after, before)
			}
		})
	}
}

// Without --profile no saved file is read: a refused copy.json in the
// default folder changes nothing.
func TestNoProfileReadsNothing(t *testing.T) {
	root := savedConfigRoot(t)
	writeSavedCopy(t, root, `{"version":2}`)
	old := profileRoot
	t.Cleanup(func() { profileRoot = old })
	profileRoot = func() (string, error) { t.Error("read the default profile without --profile"); return root, nil }
	for name, args := range fileArgs(t) {
		if o, err := parse(args, io.Discard); err != nil || o.file == nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// On this network, the profile carries the serial saved with the address,
// and only there: the proxy route's saved serial is not a LAN serial.
func TestDefaultProfileLANSerial(t *testing.T) {
	root := savedConfigRoot(t)
	body, _ := fullCopyJSON(t)
	writeSavedCopy(t, root, body)
	useProfileRoot(t, root, nil)
	if got, err := loadProfile("default"); err != nil || got.LANSerial != "" {
		t.Errorf("the proxy route = %+v, %v", got, err)
	}
	s := ui.Settings{Version: 1, Bind: "127.0.0.2", Idle: "2m", Ports: []proxyrun.Port{{Local: 1445, Device: 445}},
		Route: "lan", LANAddress: "192.0.2.7", LANSerial: "SYNTHETIC_LAN_SERIAL"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeSavedConfig(t, root, b)
	got, err := loadProfile("default")
	if err != nil || got.Endpoint != "192.0.2.7:445" || got.LANSerial != "SYNTHETIC_LAN_SERIAL" || got.LANScan != "" {
		t.Errorf("this network = %+v, %v", got, err)
	}
	// The profile carries when to scan as saved; the CLI reads a value it
	// doesn't know as start.
	for _, scan := range []string{"start", "connect", "off", "a newer mode"} {
		s.LANScan = scan
		if b, err = json.Marshal(s); err != nil {
			t.Fatal(err)
		}
		writeSavedConfig(t, root, b)
		if got, err = loadProfile("default"); err != nil || got.LANScan != scan {
			t.Errorf("lan_scan %q = %+v, %v", scan, got, err)
		}
	}
	s.LANScan = ""
	s.LANSerial = ""
	if b, err = json.Marshal(s); err != nil {
		t.Fatal(err)
	}
	writeSavedConfig(t, root, b)
	if got, err = loadProfile("default"); err != nil || got.Endpoint != "192.0.2.7:445" || got.LANSerial != "" {
		t.Errorf("no saved serial = %+v, %v", got, err)
	}
}

// With a saved profile, a file command parses with no flag but --profile,
// while the profile's window serves its saved endpoint.
func TestDefaultProfileFillsFlags(t *testing.T) {
	root := savedConfigRoot(t)
	body, _ := fullCopyJSON(t)
	writeSavedCopy(t, root, body)
	useProfileRoot(t, root, nil)
	fakeWindow(t, root, "127.0.0.2", "[1445]")
	for _, cmd := range copyCommandNames {
		if o, err := parse([]string{cmd, "--profile", "default"}, io.Discard); err != nil || o.file == nil {
			t.Errorf("%s --profile default: %v", cmd, err)
		}
	}
}

// The profile's folder is that of the telescope config.json saves: by its
// serial on either route, or its address saved without one. A telescope
// with no folder while another has one is a usage error that names
// neither, unless --dest is given.
func TestDefaultProfileTelescopeFolder(t *testing.T) {
	root := savedConfigRoot(t)
	useProfileRoot(t, root, nil)
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/A","telescopes":{
"serial:SYNTH-A":{"folder":"/synthetic/A"},"address:192.0.2.8:445":{"folder":"/synthetic/B"}}}`)
	lan := func(address, serial string) ui.Settings {
		return ui.Settings{Version: 1, Bind: "127.0.0.2", Idle: "2m", Ports: []proxyrun.Port{{Local: 1445, Device: 445}},
			Route: "lan", LANAddress: address, LANSerial: serial}
	}
	account := ui.Settings{Version: 1, Bind: "127.0.0.2", Idle: "2m", Serial: "SYNTH-A", Ports: []proxyrun.Port{{Local: 1445, Device: 445}}}
	for _, tc := range []struct {
		name     string
		config   ui.Settings
		dest     string
		noFolder bool
	}{
		{"lan serial", lan("192.0.2.7", "SYNTH-A"), "/synthetic/A", false},
		{"account serial", account, "/synthetic/A", false},
		{"lan address", lan("192.0.2.8", ""), "/synthetic/B", false},
		{"lan serial without a folder", lan("192.0.2.8", "SYNTH-C"), "", true},
		{"lan address without a folder", lan("192.0.2.9", ""), "", true},
	} {
		b, err := json.Marshal(tc.config)
		if err != nil {
			t.Fatal(err)
		}
		writeSavedConfig(t, root, b)
		if got, err := loadProfile("default"); err != nil || got.Dest != tc.dest || got.NoFolder != tc.noFolder {
			t.Errorf("%s = %+v, %v", tc.name, got, err)
		}
	}

	// The last config.json's telescope has no folder. Browse lists without
	// statuses; every other command needs one.
	before := profileFiles(t, root)
	for _, cmd := range copyCommandNames {
		if cmd == "browse" {
			if o, err := parse([]string{cmd, "--profile", "default"}, io.Discard); err != nil || o.file == nil {
				t.Errorf("browse without a folder: %v", err)
			}
			continue
		}
		code, record, logs := usageExit(t, cmd, "--profile", "default")
		if code != 2 || record.Option != "--profile" || !strings.Contains(record.Rule, "has no folder yet") || !strings.Contains(record.Rule, "--dest") ||
			strings.Contains(logs, "synthetic") || strings.Contains(logs, "192.0.2") || strings.Contains(logs, "SYNTH-") {
			t.Errorf("%s: exit %d %+v\n%s", cmd, code, record, logs)
		}
		// A given --dest wins.
		if o, err := parse([]string{cmd, "--profile", "default", "--dest", t.TempDir()}, io.Discard); err != nil || o.file == nil {
			t.Errorf("%s --dest: %v", cmd, err)
		}
	}
	if after := profileFiles(t, root); !maps.Equal(after, before) {
		t.Errorf("the state folder changed: %v, was %v", after, before)
	}

	// Before any telescope has a folder of its own, copy.json's folder is
	// the saved telescope's, whatever it is.
	if err := os.Remove(filepath.Join(root, "copy.json")); err != nil {
		t.Fatal(err)
	}
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/A"}`)
	if got, err := loadProfile("default"); err != nil || got.Dest != "/synthetic/A" || got.NoFolder {
		t.Errorf("an older copy.json = %+v, %v", got, err)
	}
}

// The default profile carries copy.json's fallbacks, then config.json's,
// for the command to log, and reading them writes neither file.
func TestDefaultProfileFallbacks(t *testing.T) {
	root := savedConfigRoot(t)
	writeSavedConfig(t, root, []byte(`{"version":1,"serial":"INVALID serial","ports":[{"device":445,"local":1445}]}`))
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/Seestar","files_at_once":57}`)
	useProfileRoot(t, root, nil)
	before := profileFiles(t, root)
	got, err := loadProfile("default")
	want := []diagnostic.Fallback{
		{File: "copy.json", Setting: "files_at_once", Fallback: "4"},
		{File: "config.json", Setting: "serial", Fallback: "none", Value: "INVALID serial"},
	}
	if err != nil || got.Workers != 4 || got.Endpoint != "127.0.0.1:1445" || !reflect.DeepEqual(got.Fallbacks, want) {
		t.Fatalf("= %+v, %v; want fallbacks %+v", got, err, want)
	}
	if after := profileFiles(t, root); !maps.Equal(after, before) {
		t.Errorf("the state folder changed: %v, was %v", after, before)
	}
}

// writeFallbackProfile saves a profile whose load falls back twice: copy.json's
// files_at_once and config.json's serial.
func writeFallbackProfile(t *testing.T) {
	root := savedConfigRoot(t)
	writeSavedConfig(t, root, []byte(`{"version":1,"serial":"INVALID serial","ports":[{"device":445,"local":1445}]}`))
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/Seestar","files_at_once":57}`)
	useProfileRoot(t, root, nil)
}

// checkProfileWarnings fails unless logs hold exactly the profile's two
// fallback warnings, the serial's naming it.
func checkProfileWarnings(t *testing.T, err error, logs *bytes.Buffer) {
	t.Helper()
	s := logs.String()
	if strings.Count(s, "settings_fallback") != 2 || !strings.Contains(s, "setting=files_at_once") || !strings.Contains(s, `setting=serial fallback=none value="INVALID serial"`) {
		t.Fatalf("want two warnings; err=%v logs=%s", err, logs)
	}
}

// A profile that loads with fallbacks and is then refused by the command's
// own validation still logs each warning once.
func TestProfileFallbackWarningsBeforeOptionRefusal(t *testing.T) {
	for _, args := range [][]string{
		{"find", "--format=invalid"}, {"sync", "--workers=0"},
		{"browse", "--listen=localhost:0"}, {"delete", "--older-than=-1s"},
	} {
		t.Run(args[0], func(t *testing.T) {
			writeFallbackProfile(t)
			var logs bytes.Buffer
			err := runWithListener(t.Context(), append(args, "--profile", "default"), io.Discard, &logs, nil, nil)
			if commandExitCode(options{command: commandFiles}, err) != 2 {
				t.Fatalf("expected configuration refusal, got %v", err)
			}
			checkProfileWarnings(t, err, &logs)
		})
	}
}

// A file command that validated logs its profile's warnings when it runs;
// a failure at run time must not log them again.
func TestProfileFallbackWarningsOnceOnRunFailure(t *testing.T) {
	noReadOnlyGuard(t)
	writeFallbackProfile(t)
	var logs bytes.Buffer
	err := runWithListener(t.Context(), []string{"find", "--profile", "default", "--endpoint", "127.0.0.1:1"}, io.Discard, &logs, nil, nil)
	if commandExitCode(options{command: commandFiles}, err) != 1 {
		t.Fatalf("expected a run failure, got %v", err)
	}
	checkProfileWarnings(t, err, &logs)
}

// ui --profile runs the app on that profile's folder, found as the file
// commands find it; an unknown name, or --state-dir too, is exit 2.
func TestUIProfileFlag(t *testing.T) {
	saved := registerUICommand
	t.Cleanup(func() { registerUICommand = saved })
	registerUICommand = func(root *cobra.Command, o *options) { root.AddCommand(newUICommand(o)) }
	root := savedConfigRoot(t)
	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	useProfileRoot(t, root, nil)
	for _, name := range []string{"Garden", "GARDEN"} {
		o, err := parse([]string{"ui", "--profile", name}, io.Discard)
		if err != nil || o.command != commandUI || o.stateDir != ui.ProfileDir(root, "Garden") || o.profile != "Garden" {
			t.Errorf("%s: %+v, %v", name, o, err)
		}
	}
	if o, err := parse([]string{"ui", "--profile", "default"}, io.Discard); err != nil || o.stateDir != root || o.profile != "default" {
		t.Errorf("default: %q %q, %v", o.stateDir, o.profile, err)
	}
	if o, err := parse([]string{"ui"}, io.Discard); err != nil || o.stateDir != "" || o.profile != "" {
		t.Errorf("no flag: %q %q, %v", o.stateDir, o.profile, err)
	}
	for _, tc := range []struct {
		args []string
		rule string
	}{
		{[]string{"ui", "--profile", "TEST0002"}, "no profile named TEST0002; profiles: default, Garden"},
		{[]string{"ui", "--profile", "Garden", "--state-dir", root}, "give --profile or --state-dir, not both"},
		{[]string{"ui", "--state-dir", root, "--profile", "default"}, "give --profile or --state-dir, not both"},
	} {
		code, record, _ := usageExit(t, tc.args...)
		if code != 2 || record.Msg != "ui failed" || record.Option != "--profile" || record.Rule != tc.rule {
			t.Errorf("%q: exit %d %+v", tc.args, code, record)
		}
	}
}

// A profile's saved account-route endpoint is this computer's port, and
// the copier checks no serial there: --profile fills it only while the
// window serving it at the saved bind address is the profile's own, or
// another profile's whose run reached the telescope it saved. Any other
// holder, none, one still starting or one that can't be asked is refused;
// a given --endpoint is not checked.
func TestProfileEndpointOwnership(t *testing.T) {
	root := savedConfigRoot(t)
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/default"}`)
	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	garden := ui.ProfileDir(root, "Garden")
	b, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeSavedConfig(t, garden, b)
	writeSavedCopy(t, garden, `{"version":1,"folder":"/synthetic/Garden"}`)
	useProfileRoot(t, root, nil)
	check := func(name string) error {
		t.Helper()
		got, err := loadProfile(name)
		if err != nil || got.CheckEndpoint == nil || got.Endpoint != "127.0.0.2:1445" {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
		return got.CheckEndpoint(t.Context())
	}
	const unknown = "no profile's app window serves the saved endpoint"
	if err := check("Garden"); err == nil || !strings.HasPrefix(err.Error(), unknown) {
		t.Errorf("no window open: %v", err)
	}
	code, record, _ := usageExit(t, "find", "--profile", "Garden")
	if code != 2 || record.Option != "--profile" || !strings.HasPrefix(record.Rule, unknown) ||
		!strings.HasSuffix(record.Rule, "give --endpoint to copy through another proxy deliberately") {
		t.Errorf("find: exit %d %+v", code, record)
	}
	if o, err := parse([]string{"find", "--profile", "Garden", "--endpoint", "192.0.2.7:445"}, io.Discard); err != nil || o.file == nil {
		t.Errorf("a given --endpoint: %v", err)
	}

	var fields atomic.Pointer[string]
	set := func(s string) { fields.Store(&s) }
	set(`"serving_address":"","serving_ports":[]`)
	fakeWindowFunc(t, root, func() string { return *fields.Load() })
	for _, tc := range []struct {
		name, fields, want string
	}{
		{"Default serves nothing", `"serving_address":"","serving_ports":[]`, unknown},
		{"Default serves another address", `"serving_address":"127.0.0.1","serving_ports":[1445]`, unknown},
		{"Default still starting", `"serving_address":"127.0.0.2","serving_ports":[1445],"serving_serial":""`,
			"the Default window serves this computer's Telescope Network ports but hasn't reached its telescope yet"},
		{"Default reached another telescope", `"serving_address":"127.0.0.2","serving_ports":[1445],"serving_serial":"OTHER_SERIAL"`,
			"the Default window serves this computer's Telescope Network ports for another telescope than this profile saved"},
		{"Default reached the saved telescope", `"serving_address":"::ffff:127.0.0.2","serving_ports":[1445],"serving_serial":"SYNTHETIC_SERIAL"`, ""},
	} {
		set(tc.fields)
		if err := check("Garden"); tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.want)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	set(`"serving_address":"127.0.0.2","serving_ports":[1445],"serving_serial":""`)
	if err := check("default"); err != nil {
		t.Errorf("default's own window, starting: %v", err)
	}

	// A window recorded that doesn't answer can't be asked.
	set(`"serving_address":"","serving_ports":[]`)
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone.Close()
	f, err := privatefile.Open(filepath.Join(garden, "ui-runtime.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, `{"port":`+strconv.Itoa(gone.Addr().(*net.TCPAddr).Port)+`,"token":"`+syntheticToken+`"}`)
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	if err := check("Garden"); err == nil || !strings.HasPrefix(err.Error(), "which app window serves the saved endpoint couldn't be checked: couldn't ask the Garden window: ") {
		t.Errorf("Garden can't be asked: %v", err)
	}
	if err := os.Remove(filepath.Join(garden, "ui-runtime.json")); err != nil {
		t.Fatal(err)
	}
	fakeWindow(t, garden, "127.0.0.2", "[1445]")
	if err := check("Garden"); err != nil {
		t.Errorf("Garden's own window: %v", err)
	}
	if o, err := parse([]string{"find", "--profile", "Garden"}, io.Discard); err != nil || o.file == nil {
		t.Errorf("find from Garden's own window: %v", err)
	}
}

// With Default alone, a profile's account-route endpoint is filled as it
// always was, whatever holds the port, as for a headless proxy's: the
// check passes without asking any window. Once a named profile exists the
// same check, asked again as every pass asks it, refuses a holder no
// window owns.
func TestProfileEndpointDefaultAlone(t *testing.T) {
	root := savedConfigRoot(t)
	writeSavedCopy(t, root, `{"version":1,"folder":"/synthetic/default"}`)
	useProfileRoot(t, root, nil)
	got, err := loadProfile("default")
	if err != nil || got.CheckEndpoint == nil || got.Endpoint != "127.0.0.2:1445" {
		t.Fatalf("%+v, %v", got, err)
	}
	if err := got.CheckEndpoint(t.Context()); err != nil {
		t.Errorf("Default alone: %v", err)
	}
	if o, err := parse([]string{"find", "--profile", "default"}, io.Discard); err != nil || o.file == nil {
		t.Errorf("find with Default alone: %v", err)
	}

	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	const unknown = "no profile's app window serves the saved endpoint"
	if err := got.CheckEndpoint(t.Context()); err == nil || !strings.HasPrefix(err.Error(), unknown) {
		t.Errorf("the next pass, with Garden: %v", err)
	}
	code, record, _ := usageExit(t, "find", "--profile", "default")
	if code != 2 || record.Option != "--profile" || !strings.HasPrefix(record.Rule, unknown) {
		t.Errorf("find with Garden: exit %d %+v", code, record)
	}
}
