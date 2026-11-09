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
// folder, history and Copy options from copy.json, and the endpoint from
// config.json, with the serial saved with it on this network. It writes
// and locks nothing. Without copy.json there is
// nothing to fill, so it is refused; without a usable config.json only the
// endpoint stays unfilled.
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
	if config, err := ui.LoadSettings(root); err == nil {
		saved.Endpoint, _ = ui.SavedCopyEndpoint(config)
		saved.LANSerial = ui.SavedLANSerial(config)
		saved.LANScan = ui.SavedLANScan(config)
	}
	return saved, nil
}
