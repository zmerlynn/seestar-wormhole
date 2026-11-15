package ui

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

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
	ServingPorts  []uint16 `json:"serving_ports"`
	TelescopeName string   `json:"telescope_name"`
}

// probeStatus asks the app of the state folder dir for its status: ok is
// false when none answers there.
func probeStatus(ctx context.Context, client *http.Client, dir string) (windowStatus, bool) {
	var record runtimeRecord
	if err := (&storage{root: dir}).read("ui-runtime.json", &record); err != nil || !record.valid() {
		return windowStatus{}, false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+record.host()+"/api/status", nil)
	if err != nil {
		return windowStatus{}, false
	}
	req.Header.Set("X-Seestar-Token", record.Token)
	response, err := client.Do(req)
	if err != nil {
		return windowStatus{}, false
	}
	defer response.Body.Close()
	var status windowStatus
	if response.StatusCode != http.StatusOK || json.UnmarshalRead(io.LimitReader(response.Body, 16<<20), &status) != nil {
		return windowStatus{}, false
	}
	return status, true
}

// ServingWindow is the open window of another of root's profiles than
// self whose run listens on the local port: the app holding it. A port
// held by anything else, or by self, has none.
func ServingWindow(ctx context.Context, root, self string, port uint16) (proxy.Holder, bool) {
	names, _ := Profiles(root)
	client := probeClient()
	defer client.CloseIdleConnections()
	for _, name := range append([]string{DefaultProfile}, names...) {
		if strings.EqualFold(name, self) {
			continue
		}
		if status, ok := probeStatus(ctx, client, ProfileDir(root, name)); ok && slices.Contains(status.ServingPorts, port) {
			return proxy.Holder{Profile: ProfileLabel(name), Telescope: status.TelescopeName}, true
		}
	}
	return proxy.Holder{}, false
}

// PortHolder is the open window of another profile that holds the local
// port, for the UI run under ctx: see ServingWindow. A run of no profile
// has none.
func PortHolder(ctx context.Context, port uint16) (proxy.Holder, bool) {
	p, ok := profileOf(ctx)
	if !ok {
		return proxy.Holder{}, false
	}
	return ServingWindow(ctx, p.root, p.name, port)
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
