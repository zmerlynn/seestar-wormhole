package main

import (
	"errors"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// profileRoot is the profiles' state root: the app's state folder
// without --state-dir, which is the default profile's folder. Tests
// replace it.
var profileRoot = func() (string, error) { return ui.StateRoot("") }

// profileDir is the state folder of the profile named name, and its name
// as its folder is named, or an exit 2 for --profile when there is none:
// a name with no folder is refused with the names there are.
func profileDir(name string) (dir, found string, err error) {
	root, err := profileRoot()
	if err != nil {
		return "", "", &argumentError{Option: "--profile", Rule: "no per-user config folder for the app's profiles; give the flags"}
	}
	dir, found, err = ui.FindProfile(root, name)
	if missing, ok := errors.AsType[*ui.NoProfileError](err); ok {
		return "", "", &argumentError{Option: "--profile", Rule: missing.Error()}
	} else if err != nil {
		return "", "", &argumentError{Option: "--profile", Rule: "the app's profiles folder couldn't be read: " + err.Error()}
	}
	return dir, found, nil
}

// loadProfile reads the profile named name for --profile: the history
// and Copy options from its copy.json, the endpoint from its config.json,
// with the serial saved with it on this network, and the folder of the
// telescope config.json saves. default is the app's state folder itself.
// It writes and locks nothing. Without copy.json there is nothing to
// fill, so it is refused; without a usable config.json the endpoint stays
// unfilled, and the folder is copy.json's folder only until any telescope
// has a folder of its own: after that, none is filled, and NoFolder is
// set.
func loadProfile(name string) (cli.Saved, error) {
	root, _, err := profileDir(name)
	if err != nil {
		return cli.Saved{}, err
	}
	set, fallbacks, err := ui.LoadCopySettings(root)
	if errors.Is(err, ui.ErrNoCopySettings) {
		return cli.Saved{}, &argumentError{Option: "--profile", Rule: "no saved Copy settings; save them on the app's Copy tab, or give the flags"}
	} else if err != nil {
		return cli.Saved{}, &argumentError{Option: "--profile", Rule: "use a valid saved copy.json in the app's state folder"}
	}
	saved := cli.Saved{
		Dest: set.Folder, HistoryDir: set.HistoryDir, Excludes: set.Excludes, Includes: set.Includes,
		NoDefaultExcludes: !set.SkipBuiltIn, Once: set.Once, Workers: set.FilesAtOnce, Fallbacks: fallbacks,
	}
	if set.IntervalMinutes > 0 {
		saved.Interval = time.Duration(set.IntervalMinutes) * time.Minute
	}
	key := ""
	if config, fallbacks, err := ui.LoadSettings(root); err == nil {
		saved.Fallbacks = append(saved.Fallbacks, fallbacks...)
		saved.Endpoint, _ = ui.SavedCopyEndpoint(config)
		saved.LANSerial = ui.SavedLANSerial(config)
		saved.LANScan = ui.SavedLANScan(config)
		key = ui.SavedTelescope(config)
	}
	// Until a telescope has a folder of its own, every telescope's folder
	// is copy.json's folder; after, "" while that is set means the saved
	// telescope has none yet, and another does.
	saved.Dest = set.FolderFor(key)
	saved.NoFolder = saved.Dest == "" && set.Folder != ""
	return saved, nil
}
