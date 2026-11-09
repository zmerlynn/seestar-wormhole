package main

import (
	"errors"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// defaultProfileRoot is the default profile's folder: the app's state
// folder without --state-dir. Tests replace it.
var defaultProfileRoot = func() (string, error) { return ui.StateRoot("") }

// loadDefaultProfile reads the default profile for --profile default: the
// history and Copy options from copy.json, the endpoint from config.json,
// with the serial saved with it on this network, and the folder of the
// telescope config.json saves. It writes and locks nothing. Without
// copy.json there is nothing to fill, so it is refused; without a usable
// config.json the endpoint stays unfilled and the folder is copy.json's
// folder, the first telescope's.
func loadDefaultProfile() (cli.Saved, error) {
	root, err := defaultProfileRoot()
	if err != nil {
		return cli.Saved{}, &argumentError{Option: "--profile", Rule: "no per-user config folder for the default profile; give the flags"}
	}
	set, err := ui.LoadCopySettings(root)
	if errors.Is(err, ui.ErrNoCopySettings) {
		return cli.Saved{}, &argumentError{Option: "--profile", Rule: "no saved Copy settings; save them on the app's Copy tab, or give the flags"}
	} else if err != nil {
		return cli.Saved{}, &argumentError{Option: "--profile", Rule: "use a valid saved copy.json in the app's state folder"}
	}
	saved := cli.Saved{
		Dest: set.Folder, HistoryDir: set.HistoryDir, Excludes: set.Excludes, Includes: set.Includes,
		NoDefaultExcludes: !set.SkipBuiltIn, Once: set.Once, Workers: set.FilesAtOnce,
	}
	if set.IntervalMinutes > 0 {
		saved.Interval = time.Duration(set.IntervalMinutes) * time.Minute
	}
	key := ""
	if config, err := ui.LoadSettings(root); err == nil {
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
