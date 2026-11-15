package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// ProfileLabel is a profile's name as the app shows it: Default for
// default, else the name as typed.
func ProfileLabel(name string) string {
	if name == DefaultProfile {
		return "Default"
	}
	return name
}

// profileKey carries the profile a UI run is of.
type profileKey struct{}

// runProfile is the profile a UI run is of: its root's profiles and its
// own name. A run of a state folder given by --state-dir is of none.
type runProfile struct{ root, name string }

// WithProfile marks the UI run under ctx as the profile name of the
// profiles under root.
func WithProfile(ctx context.Context, root, name string) context.Context {
	return context.WithValue(ctx, profileKey{}, runProfile{root, name})
}

func profileOf(ctx context.Context) (runProfile, bool) {
	p, ok := ctx.Value(profileKey{}).(runProfile)
	return p, ok
}

// probeClient is how one app asks another of its state root over its
// local controls: briefly, and never through a proxy.
func probeClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 300 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// windowStatus is what an open profile's /api/status says of its run,
// read permissively.
type windowStatus struct {
	ServingPorts   []uint16 `json:"serving_ports"`
	ServingAddress string   `json:"serving_address"`
	ServingSerial  string   `json:"serving_serial"`
	TelescopeName  string   `json:"telescope_name"`
}

// errNoWindow is no open window's record in a state folder.
var errNoWindow = errors.New("no window open")

// probeStatus asks the app of the state folder dir for its status: the
// error is errNoWindow when no window recorded itself there, else why the
// one recorded didn't answer.
func probeStatus(ctx context.Context, client *http.Client, dir string) (windowStatus, error) {
	var record runtimeRecord
	if err := (&storage{root: dir}).read("ui-runtime.json", &record); errors.Is(err, fs.ErrNotExist) || err == nil && !record.valid() {
		return windowStatus{}, errNoWindow
	} else if err != nil {
		return windowStatus{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+record.host()+"/api/status", nil)
	if err != nil {
		return windowStatus{}, err
	}
	req.Header.Set("X-Seestar-Token", record.Token)
	response, err := client.Do(req)
	if err != nil {
		return windowStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return windowStatus{}, errors.New(response.Status)
	}
	var status windowStatus
	if err := json.UnmarshalRead(io.LimitReader(response.Body, 16<<20), &status); err != nil {
		return windowStatus{}, err
	}
	return status, nil
}

// serves is whether the window's run listens on the local port at the
// bind address: the same address once parsed, so 127.0.0.1 does not hold
// a port at 127.0.0.2 nor 0.0.0.0 one at 127.0.0.1.
func (w windowStatus) serves(bind string, port uint16) bool {
	return slices.Contains(w.ServingPorts, port) && sameBind(w.ServingAddress, bind)
}

func sameBind(a, b string) bool {
	x, errX := netip.ParseAddr(a)
	y, errY := netip.ParseAddr(b)
	if errX != nil || errY != nil {
		return a == b
	}
	return x.Unmap() == y.Unmap()
}

// ServingWindow is the open window of another of root's profiles than
// self whose run listens on the local port at the bind address: the app
// holding it. A port held by anything else, or by self, has none.
func ServingWindow(ctx context.Context, root, self, bind string, port uint16) (proxy.Holder, bool) {
	names, _ := Profiles(root)
	client := probeClient()
	defer client.CloseIdleConnections()
	for _, name := range append([]string{DefaultProfile}, names...) {
		if sameProfile(name, self) {
			continue
		}
		if status, err := probeStatus(ctx, client, ProfileDir(root, name)); err == nil && status.serves(bind, port) {
			return proxy.Holder{Profile: ProfileLabel(name), Telescope: status.TelescopeName}, true
		}
	}
	return proxy.Holder{}, false
}

// EndpointWindow is a profile's open window whose run serves a local
// port: its profile as the app shows it, whether it is the profile asked
// about, and the serial key of the telescope its run reached, "" while it
// starts.
type EndpointWindow struct {
	Profile   string
	Own       bool
	Telescope string
}

// EndpointHolder is the open window among all of root's profiles, self
// included, whose run listens on the local port at the bind address. ok
// is false when none answering does; err then says why a window recorded
// in a profile's folder couldn't be asked, if one couldn't.
func EndpointHolder(ctx context.Context, root, self, bind string, port uint16) (w EndpointWindow, ok bool, err error) {
	names, _ := Profiles(root)
	client := probeClient()
	defer client.CloseIdleConnections()
	for _, name := range append([]string{DefaultProfile}, names...) {
		status, probeErr := probeStatus(ctx, client, ProfileDir(root, name))
		switch {
		case probeErr == nil && status.serves(bind, port):
			w = EndpointWindow{Profile: ProfileLabel(name), Own: sameProfile(name, self)}
			if status.ServingSerial != "" {
				w.Telescope = copyapp.SerialKey(status.ServingSerial)
			}
			return w, true, nil
		case probeErr != nil && !errors.Is(probeErr, errNoWindow) && err == nil:
			err = fmt.Errorf("couldn't ask the %s window: %w", ProfileLabel(name), probeErr)
		}
	}
	return EndpointWindow{}, false, err
}

// PortHolder is the open window of another profile that holds the local
// port at the bind address, for the UI run under ctx: see ServingWindow.
// A run of no profile has none.
func PortHolder(ctx context.Context, bind string, port uint16) (proxy.Holder, bool) {
	p, ok := profileOf(ctx)
	if !ok {
		return proxy.Holder{}, false
	}
	return ServingWindow(ctx, p.root, p.name, bind, port)
}

// activateWindow brings forward the open window of the state folder dir,
// as a relaunch does, but showing it only: the window it asks never asks
// which profile to open. false is no window answering there.
func activateWindow(ctx context.Context, client *http.Client, dir string) bool {
	var record runtimeRecord
	if err := (&storage{root: dir}).read("ui-runtime.json", &record); err != nil || !record.valid() {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+record.host()+"/api/activate", strings.NewReader(`{"show":true}`))
	if err != nil {
		return false
	}
	req.Header.Set("X-Seestar-Token", record.Token)
	req.Header.Set("Origin", "http://"+record.host())
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, io.LimitReader(response.Body, 128))
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}
