package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// Set only by main: headless tests never display a native dialog.
var reportUIError = func(error) {}

func runUICommand(ctx context.Context, o options, out, logs io.Writer) error {
	root, err := ui.StateRoot(o.stateDir)
	if err == nil {
		err = ui.Run(ctx, root, out, newUIRunner(root, logs, func(ctx context.Context, cfg proxy.Config, credentials cloud.Credentials) (commandSession, error) {
			return proxy.NewWithCredentials(ctx, cfg, credentials)
		}, net.Listen, proxy.CheckTCPStateSupport))
	}
	if err != nil {
		fmt.Fprintln(logs, err)
	}
	return err
}

// newUIRunner is the adapter used by the UI controller. Its I/O boundaries can
// be replaced without opening a browser, consulting a keychain or signing in.
func newUIRunner(root string, logs io.Writer, newSession func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error), listen func(string, string) (net.Listener, error), checkTCPState func(context.Context) error) func(context.Context, ui.Settings, cloud.Credentials, *proxy.Status) error {
	return func(ctx context.Context, s ui.Settings, credentials cloud.Credentials, status *proxy.Status) error {
		idle, err := time.ParseDuration(s.Idle)
		if err != nil {
			return err
		}
		runOptions := options{command: commandUI, bind: s.Bind, idle: idle, serial: s.Serial, model: s.Model, stateDir: root, format: "json", level: "info", pauseThreshold: p2ptunnel.DefaultConfig().HighWater}
		for _, p := range s.Ports {
			if err := runOptions.ports.Set(p); err != nil {
				return err
			}
		}
		if err := validateOptions(&runOptions); err != nil {
			return err
		}
		return runProxy(ctx, runOptions, logs, func(ctx context.Context, cfg proxy.Config) (commandSession, error) {
			cfg.Status = status
			return newSession(ctx, cfg, credentials)
		}, listen, listenCommandDiscovery, checkTCPState)
	}
}
