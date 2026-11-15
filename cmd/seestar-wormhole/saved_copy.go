package main

import (
	"context"
	"errors"
	"net/netip"
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

// endpointCheck checks the endpoint config saves for the account route,
// which is this computer's port, for the profile self: whichever app
// window connected first serves it, and the copier checks no serial
// there. It passes only while the window serving the port at config's
// bind address is self's own, or another profile's whose run reached the
// telescope config saves. It is nil on this network, which dials the
// telescope's own address.
func endpointCheck(config ui.Settings, endpoint, self string) func(context.Context) error {
	if endpoint == "" || !ui.SavedThroughAccount(config) {
		return nil
	}
	const deliberately = "give --endpoint to copy through another proxy deliberately"
	addr, err := netip.ParseAddrPort(endpoint)
	root, rootErr := profileRoot()
	if err = errors.Join(err, rootErr); err != nil {
		return func(context.Context) error {
			return errors.New("which app window serves the saved endpoint couldn't be checked: " + err.Error() + "; " + deliberately)
		}
	}
	telescope := ui.SavedTelescope(config)
	return func(ctx context.Context) error {
		w, ok, err := ui.EndpointHolder(ctx, root, self, config.Bind, addr.Port())
		switch {
		case ok && (w.Own || w.Telescope != "" && w.Telescope == telescope):
			return nil
		case ok && w.Telescope == "":
			return errors.New("the " + w.Profile + " window serves this computer's Telescope Network ports but hasn't reached its telescope yet; try again once it has, or " + deliberately)
		case ok:
			return errors.New("the " + w.Profile + " window serves this computer's Telescope Network ports for another telescope than this profile saved; copy with that window's profile, or " + deliberately)
		case err != nil:
			return errors.New("which app window serves the saved endpoint couldn't be checked: " + err.Error() + "; " + deliberately)
		}
		return errors.New("no profile's app window serves the saved endpoint, so which telescope it reaches is unknown: a proxy run from the command line or another program may hold the port; open the profile's window, or " + deliberately)
	}
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
	root, found, err := profileDir(name)
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
		saved.CheckEndpoint = endpointCheck(config, saved.Endpoint, found)
	}
	// Until a telescope has a folder of its own, every telescope's folder
	// is copy.json's folder; after, "" while that is set means the saved
	// telescope has none yet, and another does.
	saved.Dest = set.FolderFor(key)
	saved.NoFolder = saved.Dest == "" && set.Folder != ""
	return saved, nil
}
