package main

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// useProfileRoot makes root the default profile's folder for the test.
func useProfileRoot(t *testing.T, root string, err error) {
	old := defaultProfileRoot
	t.Cleanup(func() { defaultProfileRoot = old })
	defaultProfileRoot = func() (string, error) { return root, err }
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
	got, err := loadDefaultProfile()
	want := cli.Saved{
		Dest: "/synthetic/Seestar", HistoryDir: history, Endpoint: "127.0.0.2:1445",
		Excludes: []string{"copy-exclude"}, Includes: []string{"copy-include"}, NoDefaultExcludes: true,
		Interval: 9 * time.Minute, Once: true, Workers: 3,
	}
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
	got, err = loadDefaultProfile()
	if err != nil || got.HistoryDir != "" || got.NoDefaultExcludes || got.Dest != "/synthetic/Seestar" || got.Workers != 4 || got.Interval != 5*time.Minute {
		t.Errorf("a short copy.json = %+v, %v", got, err)
	}

	// Without a usable config.json, only the endpoint is left unfilled.
	writeSavedConfig(t, root, []byte(`{"version":99}`))
	if got, err = loadDefaultProfile(); err != nil || got.Endpoint != "" || got.Dest == "" {
		t.Errorf("a refused config.json = %+v, %v", got, err)
	}
	if err := os.Remove(filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	if got, err = loadDefaultProfile(); err != nil || got.Endpoint != "" || got.Dest == "" {
		t.Errorf("no config.json = %+v, %v", got, err)
	}
	code, record, _ := usageExit(t, "sync", "--profile", "default")
	if code != 2 || record.Option != "--endpoint" || record.Rule != "required; give the SMB host:port" {
		t.Errorf("sync without a saved endpoint: exit %d %+v", code, record)
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
		{"no config folder", "", "", errors.New("no config folder"), "no per-user config folder for the default profile; give the flags"},
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
	old := defaultProfileRoot
	t.Cleanup(func() { defaultProfileRoot = old })
	defaultProfileRoot = func() (string, error) { t.Error("read the default profile without --profile"); return root, nil }
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
	if got, err := loadDefaultProfile(); err != nil || got.LANSerial != "" {
		t.Errorf("the proxy route = %+v, %v", got, err)
	}
	s := ui.Settings{Version: 1, Bind: "127.0.0.2", Idle: "2m", Ports: []proxyrun.Port{{Local: 1445, Device: 445}},
		Route: "lan", LANAddress: "192.0.2.7", LANSerial: "SYNTHETIC_LAN_SERIAL"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeSavedConfig(t, root, b)
	got, err := loadDefaultProfile()
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
		if got, err = loadDefaultProfile(); err != nil || got.LANScan != scan {
			t.Errorf("lan_scan %q = %+v, %v", scan, got, err)
		}
	}
	s.LANScan = ""
	s.LANSerial = ""
	if b, err = json.Marshal(s); err != nil {
		t.Fatal(err)
	}
	writeSavedConfig(t, root, b)
	if got, err = loadDefaultProfile(); err != nil || got.Endpoint != "192.0.2.7:445" || got.LANSerial != "" {
		t.Errorf("no saved serial = %+v, %v", got, err)
	}
}

// With a saved profile, a file command parses with no flag but --profile.
func TestDefaultProfileFillsFlags(t *testing.T) {
	root := savedConfigRoot(t)
	body, _ := fullCopyJSON(t)
	writeSavedCopy(t, root, body)
	useProfileRoot(t, root, nil)
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
		if got, err := loadDefaultProfile(); err != nil || got.Dest != tc.dest || got.NoFolder != tc.noFolder {
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
	if got, err := loadDefaultProfile(); err != nil || got.Dest != "/synthetic/A" || got.NoFolder {
		t.Errorf("an older copy.json = %+v, %v", got, err)
	}
}
