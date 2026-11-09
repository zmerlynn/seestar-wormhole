package ui

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// runFunc runs the proxy for one run. It calls attach with the session's
// dialer before arming the status, and runs the detach it returns before
// closing the session; a refused attach means the run is already
// stopping, and it returns without arming.
type runFunc func(ctx context.Context, s Settings, credentials cloud.Credentials, status *proxy.Status, attach func(proxy.Dialer) (detach func(), ok bool)) error
type controller struct {
	mu                 sync.Mutex
	parent             context.Context
	run                runFunc
	credentials        *credentialStore
	done               chan struct{}
	cancel             context.CancelFunc
	status             *proxy.Status
	stopping, quitting bool
	final              string
	failure            *ActionError
	devices            []cloudhttp.DeviceChoice
	devicesEmail       string
	save               func(Settings) error
	log                *eventLog
	runSettings        Settings
	runs               uint64      // numbers each run, for the copier's target
	files              attachments // the run's channel for the file tools
	saveAttempted      bool
	saveMu             sync.Mutex // Serializes snapshot writes, never status or Stop.
	pendingSave        *Settings
	lastSaved          Settings
	// routeGen numbers the explicit saves of the telescope: route saves
	// and changes of when to scan for it (see server.saveSettings). A
	// telescope's new address found by a search that started before one
	// is not saved over it, nor reused by a run after it. A moved address
	// saved by persistMoved is no explicit save.
	routeGen atomic.Uint64
	// text reads a run's status text for isReady; nil is its Text. Tests
	// replace it, since only a proxy session makes a status Ready.
	text func(*proxy.Status) string
}

func (c *controller) start(s Settings, password string) error {
	s.Email = strings.TrimSpace(strings.ReplaceAll(s.Email, " ", ""))
	if s.Email == "" {
		return &ActionError{Code: "email", Field: "email"}
	}
	if _, _, err := s.validate(); err != nil {
		return err
	}
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil || c.quitting {
		return errors.New("Stop the current run before starting again")
	}
	ctx, cancel := context.WithCancel(c.parent)
	c.cancel = cancel
	c.done = make(chan struct{})
	c.status = proxy.NewStatus()
	c.status.Catalog = c.log.addCatalog
	c.stopping = false
	c.final = ""
	c.failure = nil
	s.Ports = append([]proxyrun.Port(nil), s.Ports...)
	if c.devicesEmail != s.Email {
		c.devices = nil
	}
	c.devicesEmail = s.Email
	c.runSettings = s
	c.runs++
	c.files.begin(ctx, c.runs)
	c.saveAttempted = false
	done, status, id := c.done, c.status, c.runs
	attach := func(d proxy.Dialer) (func(), bool) { return c.files.attach(id, d) }
	go func() {
		finished := make(chan struct{})
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			for {
				select {
				case text := <-status.Updates():
					c.recordProgress(telescopeText(text, status.SelectedDevice()))
					c.saveAtBoundary(status, s)
				case <-finished:
					c.saveAtBoundary(status, s)
					return
				}
			}
		}()
		c.record("Connecting to your Seestar…")
		defer cancel()
		credentials, err := c.credentials.credentials(ctx, s, password)
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err == nil {
			err = runObserved(cancel, status.Rejected(), func() error { return c.run(ctx, s, credentials, status, attach) })
		}
		c.files.finish(id)
		close(finished)
		<-watched
		c.mu.Lock()
		if ctx.Err() != nil && err == nil {
			err = ctx.Err()
		}
		c.final = finalStatus(err)
		c.failure = runFailure(err)
		if c.failure != nil && c.failure.Code == "selection_multiple" {
			c.devices = append([]cloudhttp.DeviceChoice(nil), c.failure.Devices...)
		}
		if diagnostic.CleanupError(err) != nil {
			c.failure = &ActionError{Code: "release"}
		}
		if c.final == "Stopped" {
			c.final = "Stopped. Seestar may still be releasing the session."
		}
		final := c.final
		c.mu.Unlock()
		c.log.addFailure(final, err)
		c.mu.Lock()
		c.done = nil
		c.cancel = nil
		close(done)
		c.mu.Unlock()
	}()
	return nil
}
func runFailure(err error) *ActionError {
	if diagnostic.CleanupError(err) != nil {
		return &ActionError{Code: "release"}
	}
	if err == nil {
		return nil
	}
	e := actionError(err)
	if e.Code != "connection" || !errors.Is(err, context.Canceled) {
		return e
	}
	return nil
}
func finalStatus(err error) string {
	if e := runFailure(err); e != nil {
		return errorCopy(e.Code)
	}
	return "Stopped"
}
func (c *controller) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.stopping = true
		c.files.stop(c.runs)
		c.cancel()
	}
}

// quit joins cleanup even when startup has not yet returned a session.
func (c *controller) requestQuit() {
	c.mu.Lock()
	c.quitting = true
	if c.cancel != nil {
		c.stopping = true
		c.files.stop(c.runs)
		c.cancel()
	}
	c.mu.Unlock()
}
func (c *controller) quit() {
	c.requestQuit()
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (c *controller) snapshot() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}
func (c *controller) snapshotLocked() (string, bool) {
	if c.done == nil {
		if c.final != "" {
			return c.final, false
		}
		return "Not connected", false
	}
	if c.stopping {
		return "Stopping — releasing the Seestar session…", true
	}
	if p := c.credentials.state(c.runSettings.Email); p.Code == "password_reading" {
		return "Reading saved password…", true
	}
	return telescopeText(c.status.Text(), c.status.SelectedDevice()), true
}

// A terminal credential/protocol rejection ends this UI run after cleanup joins.
// The headless command retains its existing ownership and exit behavior.
func runObserved(cancel context.CancelFunc, rejected <-chan struct{}, run func() error) error {
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-rejected:
		cancel()
		return <-done
	}
}

// A queued Ready update must not announce a new connection during Stop.
func (c *controller) recordProgress(text string) {
	l := c.log
	if l == nil {
		return
	}
	// Wait for the event owner before taking the Stop/status mutex. Publication
	// and Stop are ordered in memory; the subsequent disk append holds neither.
	l.diskMu.Lock()
	defer l.diskMu.Unlock()
	l.mu.Lock()
	c.mu.Lock()
	var entry *event
	if !c.stopping {
		entry = l.publishLocked(text, nil, c.status.SelectedDevice())
	}
	c.mu.Unlock()
	l.mu.Unlock()
	l.write(entry)
}
func (c *controller) record(text string) {
	if c.log != nil {
		c.log.add(text)
	}
}
func (c *controller) saveAtBoundary(status *proxy.Status, s Settings) {
	if !status.SaveBoundary(s.lazy()) {
		return
	}
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	if c.saveAttempted {
		c.mu.Unlock()
		return
	}
	c.saveAttempted = true
	c.mu.Unlock()
	// The run is ready: the account route has connected.
	s.RouteEstablished = true
	c.persistLocked(s)
}

// One owner retains the failed snapshot across later unsuccessful Starts.
// Explicit saves supersede it; retries never accept a page's draft settings.
func (c *controller) persistLocked(s Settings) error {
	if c.save == nil {
		return nil
	}
	s = c.credentials.association(s)
	s.Ports = append([]proxyrun.Port(nil), s.Ports...)
	err := c.save(s)
	c.mu.Lock()
	c.pendingSave = nil
	if err != nil {
		c.pendingSave = &s
	} else {
		c.lastSaved = s
	}
	c.mu.Unlock()
	if err == nil {
		c.record("Settings saved.")
	} else {
		c.record(errorCopy("storage"))
	}
	return err
}
func (c *controller) retrySave() error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if c.pendingSave == nil {
		return nil
	}
	// A retry establishes no route; the next Connect or LAN save does.
	s := *c.pendingSave
	c.mu.Lock()
	s.RouteEstablished = c.lastSaved.RouteEstablished
	c.mu.Unlock()
	return c.persistLocked(s)
}
func (c *controller) persistRemoval(generation uint64) error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if c.credentials.generationID() != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
	// Remove edits only the last valid persisted snapshot, never the form draft.
	// A first-launch removal uses the same valid defaults as initial setup.
	s := c.lastSaved
	if s.Version == 0 {
		s = defaultSettings()
	}
	err := c.persistLocked(s)
	if c.credentials.generationID() != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
	return err
}

// telescopeText interpolates only the selected display label into fixed states.
func telescopeText(text, telescope string) string {
	if telescope != "" {
		switch text {
		case "Ready":
			return "Connected to " + telescope
		case "Waiting for a local client":
			return "Waiting for an app to connect to " + telescope
		case "Busy — waiting for the other session":
			return telescope + " is occupied. Retrying automatically."
		case "Seestar not reachable":
			return telescope + " isn't reachable. Check this computer's internet connection and make sure it is awake and connected to Wi-Fi. Retrying automatically."
		case "Reconnecting":
			return "Connection to " + telescope + " interrupted. Retrying automatically."
		case "Releasing idle session":
			return "Releasing idle session with " + telescope + "…"
		case "Reconnecting — waiting for cleanup/retry":
			return "Waiting before retrying " + telescope + "…"
		}
	}
	return uiText(text)
}

func uiText(text string) string {
	switch text {
	case "Ready":
		return "Connected to your Seestar"
	case "Waiting for a local client":
		return "Waiting for an app to connect"
	case "Busy — waiting for the other session":
		return "Seestar is occupied. Retrying automatically."
	case "Seestar not reachable":
		return "Seestar isn't reachable. Check this computer's internet connection and make sure Seestar is awake and connected to Wi-Fi. Retrying automatically."
	case "Reconnecting":
		return "Connection interrupted. Retrying automatically."
	case "Releasing idle session":
		return "Releasing idle Seestar session…"
	case "Reconnecting — waiting for cleanup/retry":
		return "Waiting before retrying the connection…"
	case "Sign-in required — correct credentials and Start again":
		return errorCopy("signin")
	case "Stopped":
		return "Stopped"
	default:
		return "Connecting to your Seestar…"
	}
}

// The control model is independent of pending HTTP/keychain actions in the page.
type runView struct {
	DevicesEmail   string                   `json:"devices_email"`
	Devices        []cloudhttp.DeviceChoice `json:"devices,omitempty"`
	FailureMessage string                   `json:"failure_message,omitempty"`
	Status         string                   `json:"status"`
	SelectedDevice string                   `json:"selected_device,omitempty"`
	Active         bool                     `json:"active"`
	Stopping       bool                     `json:"stopping"`
	Cancel         bool                     `json:"cancel"`
	Ready          bool                     `json:"ready"` // the session is ready now; the file tools need only [controller.connected]
	Failure        *ActionError             `json:"failure,omitempty"`
	settings       Settings
	SaveFailed     bool `json:"save_failed"`
	Discovery      bool `json:"discovery_started"`
}

func (c *controller) view() runView {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, active := c.snapshotLocked()
	v := runView{DevicesEmail: c.devicesEmail, Devices: append([]cloudhttp.DeviceChoice(nil), c.devices...), Status: text, Active: active, Stopping: c.stopping && active, Failure: c.failure, settings: c.runSettings, SaveFailed: c.pendingSave != nil}
	if active {
		v.SelectedDevice = c.status.SelectedDevice()
		v.Cancel = !c.status.WasReady() && !c.runSettings.lazy()
		v.Ready = !c.stopping && c.isReady(c.status)
		v.Discovery = c.runSettings.Discovery && c.status.SaveBoundary(true)
	}
	if c.failure != nil {
		v.FailureMessage = errorCopy(c.failure.Code)
	}
	if !active && c.failure != nil && c.failure.Code != "release" {
		if c.failure.Code == "selection_multiple" {
			v.Status = "Choose a telescope"
		} else {
			v.Status = "Could not connect"
		}
	}
	return v
}

// persistRoute saves the route onto the last saved settings, as Remove
// does, never onto a form draft. It is refused while a run is active,
// whose save at its ready boundary would carry the old route.
func (c *controller) persistRoute(route, address, serial string) error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	running := c.done != nil
	c.mu.Unlock()
	if running {
		return &ActionError{Code: "route_locked"}
	}
	c.routeGen.Add(1)
	s := c.lastSaved
	if s.Version == 0 {
		s = defaultSettings()
	}
	s.Route = route
	if route == routeLAN {
		// The caller has tested the address.
		s.LANAddress, s.LANSerial, s.RouteEstablished = address, serial, true
	}
	return c.persistLocked(s)
}

// persistLANScan saves when to scan for the telescope on this network
// onto the last saved settings. It is refused, as persistRoute is, while a
// run is active, whose save at its ready boundary would carry the old
// value.
func (c *controller) persistLANScan(mode landiscovery.Mode) error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	running := c.done != nil
	c.mu.Unlock()
	if running {
		return &ActionError{Code: "route_locked"}
	}
	s := c.lastSaved
	if s.Version == 0 {
		s = defaultSettings()
	}
	s.LANScan = string(mode)
	return c.persistLocked(s)
}

// routeGeneration numbers the latest explicit save of the telescope.
func (c *controller) routeGeneration() uint64 { return c.routeGen.Load() }

// persistMoved saves address as this network's address after a scan found
// the telescope with serial there, when it was saved at from and no
// explicit save came after gen, the routeGeneration its connect read with
// the mode. It saves nothing when the saved settings changed meanwhile or
// a run is active, and reports whether it saved.
func (c *controller) persistMoved(from, address, serial string, gen uint64) bool {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	c.mu.Lock()
	running := c.done != nil
	s := c.lastSaved
	c.mu.Unlock()
	saved, ok := copyapp.NormalizeAddress(s.LANAddress)
	if running || !ok || s.Route != routeLAN || serial == "" || s.LANSerial != serial || saved != from || c.routeGen.Load() != gen {
		return false
	}
	s.LANAddress = address
	return c.persistLocked(s) == nil
}

// savedAt reports whether the saved route is this network at address,
// with serial.
func (c *controller) savedAt(address, serial string) bool {
	c.mu.Lock()
	s := c.lastSaved
	c.mu.Unlock()
	saved, ok := copyapp.NormalizeAddress(s.LANAddress)
	return ok && s.Route == routeLAN && saved == address && serial != "" && s.LANSerial == serial
}

// connected returns the run's settings, the telescope it selected and
// the run's number while the run is armed and attached, and false while
// it is not running, stopping, not yet armed or not attached. It does not
// wait for the session to be ready: on demand, the file tools' dials are
// what open a dormant session. The telescope's label is for comparing
// only: it names the serial.
func (c *controller) connected() (run Settings, device string, id uint64, ok bool) {
	run, device, _, id, ok = c.connectedSerial()
	return run, device, id, ok
}

// connectedSerial is connected with the selected telescope's serial, which
// keys its copy folder and is never shown or logged.
func (c *controller) connectedSerial() (run Settings, device, serial string, id uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == nil || c.stopping || !c.status.Armed() || !c.files.live(c.runs) {
		return Settings{}, "", "", 0, false
	}
	return c.runSettings, c.status.SelectedDevice(), c.status.SelectedSerial(), c.runs, true
}

func (c *controller) isReady(status *proxy.Status) bool {
	text := status.Text
	if c.text != nil {
		text = func() string { return c.text(status) }
	}
	return text() == "Ready"
}

func (c *controller) persistPassword(s Settings, generation uint64) error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if c.credentials.generationID() != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
	// A password establishes no route; the page's draft can't either.
	c.mu.Lock()
	s.RouteEstablished = c.lastSaved.RouteEstablished
	c.mu.Unlock()
	err := c.persistLocked(s)
	if err == nil {
		c.mu.Lock()
		c.saveAttempted = true
		c.mu.Unlock()
	}
	if c.credentials.generationID() != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
	return err
}
