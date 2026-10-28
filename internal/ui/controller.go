package ui

import (
	"context"
	"errors"
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
	saved, saveFailed  bool
}

func (c *controller) start(s Settings, password string) error {
	if s.Email == "" {
		return &ActionError{Code: "email", Field: "email"}
	}
	if _, _, err := s.validate(); err != nil {
		return err
	}
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
	s.Ports = append([]string(nil), s.Ports...)
	c.runSettings = s
	c.saved, c.saveFailed = false, false
	done, status := c.done, c.status
	go func() {
		finished := make(chan struct{})
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			for {
				select {
				case text := <-status.Updates():
					c.record(uiText(text))
					c.saveAtBoundary(status, s)
				case <-finished:
					c.saveAtBoundary(status, s)
					return
				}
			}
		}()
		c.record("Connecting to your telescope…")
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
		defer c.mu.Unlock()
		if ctx.Err() != nil && err == nil {
			err = ctx.Err()
		}
		c.final = finalStatus(err)
		c.failure = runFailure(err)
		if diagnostic.CleanupError(err) != nil {
			c.failure = &ActionError{Code: "release"}
		}
		if c.final == "Stopped" && status.WasReady() {
			c.final = "Stopped. You can use remote mode in the Seestar app again."
		}
		c.record(c.final)
		c.done = nil
		c.cancel = nil
		close(done)
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
		return "Stopping — releasing the telescope session…", true
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

func (c *controller) record(text string) {
	if c.log != nil {
		c.log.add(text)
	}
}
func (c *controller) saveAtBoundary(status *proxy.Status, s Settings) {
	if !status.SaveBoundary(s.lazy()) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saved || c.saveFailed {
		return
	}
	c.persist(s)
}
func (c *controller) persist(s Settings) {
	if c.save == nil {
		return
	}
	err := c.save(c.credentials.association(s))
	c.saved, c.saveFailed = err == nil, err != nil
	if err == nil {
		c.record("Settings saved.")
	} else {
		c.record(errorCopy("storage"))
	}
}
func (c *controller) retrySave() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.saveFailed {
		return &ActionError{Code: "settings"}
	}
	c.persist(c.runSettings)
	if c.saveFailed {
		return errStorage
	}
	return nil
}
func uiText(text string) string {
	switch text {
	case "Ready":
		return "Connected to your telescope"
	case "Waiting for a local client":
		return "Waiting for an app to connect"
	case "Busy — waiting for the other session":
		return "Telescope is in use. Disconnect the other remote session. Retrying automatically."
	case "Reconnecting":
		return "Connection lost. Retrying automatically."
	case "Releasing idle session":
		return "Releasing idle telescope session…"
	case "Reconnecting — waiting for cleanup/retry":
		return "Waiting for session release before retrying…"
	case "Sign-in required — correct credentials and Start again":
		return errorCopy("signin")
	case "Stopped":
		return "Stopped"
	default:
		return "Connecting to your telescope…"
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
	Settings       Settings     `json:"run_settings"`
	Saved          bool         `json:"settings_saved"`
	SaveFailed     bool         `json:"save_failed"`
	Discovery      bool         `json:"discovery_started"`
}

func (c *controller) view() runView {
	c.mu.Lock()
	defer c.mu.Unlock()
	text, active := c.snapshotLocked()
	v := runView{Status: text, Active: active, Stopping: c.stopping && active, Failure: c.failure, Settings: c.runSettings, Saved: c.saved, SaveFailed: c.saveFailed}
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
