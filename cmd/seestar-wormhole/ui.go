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

func runUICommand(ctx context.Context, o options, out, logs io.Writer) error {
	root, err := ui.StateRoot(o.stateDir)
	if err == nil {
		err = ui.Run(ctx, root, out, func(ctx context.Context, s ui.Settings, credentials cloud.Credentials, status *proxy.Status) error {
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
				return proxy.NewWithCredentials(ctx, cfg, credentials)
			}, net.Listen, listenCommandDiscovery, proxy.CheckTCPStateSupport)
		})
	}
	if err != nil {
		fmt.Fprintln(logs, err)
	}
	return err
}
