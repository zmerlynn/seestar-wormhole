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
}

func (c *controller) start(s Settings, password string) error {
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
	done, status := c.done, c.status
	go func() {
		defer cancel()
		credentials, err := c.credentials.credentials(ctx, s, password)
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err == nil {
			err = runObserved(cancel, status.Rejected(), func() error { return c.run(ctx, s, credentials, status) })
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if ctx.Err() != nil && err == nil {
			err = ctx.Err()
		}
		c.final = finalStatus(err)
		c.done = nil
		c.cancel = nil
		close(done)
	}()
	return nil
}
func finalStatus(err error) string {
	if cleanup := diagnostic.CleanupError(err); cleanup != nil {
		return "Stopped with cleanup error (" + diagnostic.CleanupClass(cleanup) + ")"
	}
	_, reason := diagnostic.Fields(err, "")
	if reason == "unauthorized" || reason == "reauthentication_required" || errors.Is(err, cloud.ErrConfig) {
		return "Sign-in required — correct credentials and Start again"
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return "Stopped with error — check settings and local port availability"
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
	if c.done == nil {
		if c.final != "" {
			return c.final, false
		}
		return "Stopped", false
	}
	if c.stopping {
		return "Stopping — releasing session", true
	}
	return c.status.Text(), true
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
