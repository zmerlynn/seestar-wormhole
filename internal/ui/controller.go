package ui

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type runFunc func(context.Context, Settings, cloud.Credentials, *proxy.Status) error
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
	save               func(Settings) error
	log                *eventLog
	runSettings        Settings
	saveAttempted      bool
	saveMu             sync.Mutex // Serializes snapshot writes, never status or Stop.
	pendingSave        *Settings
	lastSaved          Settings
}

func (c *controller) start(s Settings, password string) error {
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
	c.stopping = false
	c.final = ""
	c.failure = nil
	s.Ports = append([]proxyrun.Port(nil), s.Ports...)
	c.runSettings = s
	c.saveAttempted = false
	done, status := c.done, c.status
	go func() {
		finished := make(chan struct{})
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			for {
				select {
				case text := <-status.Updates():
					c.recordProgress(uiText(text))
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
			err = runObserved(cancel, status.Rejected(), func() error { return c.run(ctx, s, credentials, status) })
		}
		close(finished)
		<-watched
		c.mu.Lock()
		if ctx.Err() != nil && err == nil {
			err = ctx.Err()
		}
		c.final = finalStatus(err)
		c.failure = runFailure(err)
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
		c.cancel()
	}
}

// quit joins cleanup even when startup has not yet returned a session.
func (c *controller) requestQuit() {
	c.mu.Lock()
	c.quitting = true
	if c.cancel != nil {
		c.stopping = true
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
	return uiText(c.status.Text()), true
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
		entry = l.publishLocked(text, nil)
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
	return c.persistLocked(*c.pendingSave)
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
	FailureMessage string       `json:"failure_message,omitempty"`
	Status         string       `json:"status"`
	Active         bool         `json:"active"`
	Stopping       bool         `json:"stopping"`
	Cancel         bool         `json:"cancel"`
	Failure        *ActionError `json:"failure,omitempty"`
	settings       Settings
	SaveFailed     bool `json:"save_failed"`
	Discovery      bool `json:"discovery_started"`
}

func (c *controller) view() runView {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, active := c.snapshotLocked()
	v := runView{Status: text, Active: active, Stopping: c.stopping && active, Failure: c.failure, settings: c.runSettings, SaveFailed: c.pendingSave != nil}
	if active {
		v.Cancel = !c.status.WasReady() && !c.runSettings.lazy()
		v.Discovery = c.runSettings.Discovery && c.status.SaveBoundary(true)
	}
	if c.failure != nil {
		v.FailureMessage = errorCopy(c.failure.Code)
	}
	if !active && c.failure != nil && c.failure.Code != "release" {
		v.Status = "Could not connect"
	}
	return v
}

func (c *controller) persistPassword(s Settings, generation uint64) error {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if c.credentials.generationID() != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
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
