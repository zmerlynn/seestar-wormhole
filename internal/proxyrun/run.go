// Package proxyrun owns forwarding startup and joined session cleanup.
package proxyrun

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"log/slog"
	"net"
	"strconv"
)

type Session interface {
	proxy.Dialer
	Close() error
	Done() <-chan struct{}
	DeviceDetail() cloud.Detail
}
type Discovery interface {
	Serve(context.Context, *landiscovery.Machine) error
	Close() error
}
type Port struct{ Local, Device uint16 }
type Config struct {
	Bind, Revision string
	Ports          []Port
	AllInterfaces  bool
	Session        proxy.Config
}

// Hooks keep command diagnostics and test adapters at the caller boundary.
type Hooks struct {
	Phase           *string
	Cleanup         *error
	NewSession      func(context.Context, proxy.Config) (Session, error)
	Listen          func(string, string) (net.Listener, error)
	ListenDiscovery func(string, int, landiscovery.Options, *slog.Logger) (Discovery, error)
	CheckTCPState   func(context.Context) error
	Accepted        func()
	WarnLANHTTP     func()
	WarnBind        func([]proxy.Forward)
}

func Run(ctx context.Context, cfg Config, h Hooks) (result error) {
	var err error
	logger := cfg.Session.Logger
	*h.Phase = "tcp_state_query"
	if err := h.CheckTCPState(ctx); err != nil {
		return err
	}
	*h.Phase = "listener"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var forwards []proxy.Forward
	defer func() {
		for _, f := range forwards {
			f.Listener.Close()
		}
	}()
	for _, p := range cfg.Ports {
		l, err := h.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(int(p.Local))))
		if err != nil {
			return &diagnostic.OptionError{Option: "--bind", Rule: "use an OS-supported bind address with available local ports"}
		}
		forwards = append(forwards, proxy.Forward{Listener: l, Port: p.Device})
	}
	var discovery Discovery
	if cfg.Session.LANDiscovery {
		*h.Phase = "lan_discovery"
		discovery, err = h.ListenDiscovery(cfg.Bind, landiscovery.Port, landiscovery.Options{AllInterfaces: cfg.AllInterfaces}, logger)
		if err != nil {
			return &diagnostic.OptionError{Option: "--bind", Rule: "use an OS-supported LAN bind address with UDP discovery available"}
		}
		defer discovery.Close()
	}
	*h.Phase = "session"
	s, err := h.NewSession(ctx, cfg.Session)
	if err != nil {
		return err
	}
	// Always attempt cloud stop even when listener setup or a worker fails.
	defer func() {
		outcome := s.Close()
		result = errors.Join(result, outcome)
		*h.Cleanup = diagnostic.CleanupError(outcome)
		logger.Info("proxy_cleanup_complete", "cleanup_reason", diagnostic.CleanupClass(*h.Cleanup))
	}()
	var discoveryDone chan struct{}
	var discoveryErr error
	if discovery != nil {
		*h.Phase = "lan_discovery"
		d := s.DeviceDetail()
		machine, synthesized, err := landiscovery.New(d.Serial(), d.Name(), d.Model())
		if err != nil {
			return diagnostic.Wrap("lan_discovery", "invalid_device_detail", err)
		}
		h.Accepted()
		logger.Warn("lan_discovery_synthesized", "fields", synthesized, "envelope_fields", []string{"Timestamp"})
		discoveryDone = make(chan struct{})
		go func() { discoveryErr = discovery.Serve(ctx, machine); close(discoveryDone) }()
		// Join discovery before closing the session or returning from any branch.
		defer func() { cancel(); discovery.Close(); <-discoveryDone }()
		logger.Info("lan_discovery_started", "port", landiscovery.Port, "all_interfaces", cfg.AllInterfaces)
		h.WarnLANHTTP()
	}
	// Configuration is accepted before waiting for readiness, which may spend
	// a long time retrying busy. Keep those lifecycle diagnostics visible.
	h.Accepted()
	h.WarnBind(forwards)
	done := make(chan error, 1)
	go func() { done <- proxy.Serve(ctx, s, forwards, 128, logger) }()
	logger.Info("proxy_started", "revision", cfg.Revision, "listeners", len(forwards), "lazy", cfg.Session.Idle > 0, "pause_threshold", cfg.Session.PauseThreshold)
	for {
		select {
		case <-discoveryDone:
			cancel()
			err = <-done
			return errors.Join(diagnostic.Wrap("lan_discovery", "", discoveryErr), err)
		case err = <-done:
			cancel()
			return err
		case <-s.Done():
			cancel()
			return <-done
		case <-ctx.Done():
			cancel()
			return <-done
		}
	}
}
