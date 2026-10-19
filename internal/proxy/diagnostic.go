package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/oserror"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"net"
	"syscall"
)

func (x *stack) failAt(phase, reason string) {
	layer := session.Rendezvous
	switch phase {
	case "stack":
		layer = diagnostic.StackLayer
	case "dtls":
		layer = session.DTLS
	case "rdt":
		layer = session.RDT
	case "tunnel":
		layer = session.Tunnel
	}
	x.fail(diagnostic.NewTerminal(layer, reason, session.ErrTransport))
}

func (x *stack) transition(phase, before, after string) {
	if before != after && x.f.s.logger != nil {
		x.f.s.logger.Debug("phase_transition", "phase", phase, "from", before, "state", after)
	}
}

func (x *stack) pathChosen(a iotc.Action) {
	if x.f.s.logger == nil {
		return
	}
	path, transport := "relay", "udp"
	if x.iotc != nil && x.iotc.Direct() {
		path = "direct"
	}
	if a.Link.Transport == iotc.TCP {
		transport = "tcp"
	}
	x.f.s.logger.Debug("rendezvous_path", "phase", "rendezvous", "path", path, "transport", transport)
}

// Machine reasons are filtered as well: adding a new reason must never silently
// turn an error containing peer input into a loggable value.
func layerReason(reason string) string {
	switch reason {
	case "creation timeout", "local close timeout", "receive watchdog expired", "login timeout", "verification timeout":
		return "timeout"
	case "receive budget exceeded", "outbound budget or encoding failure", "RDT receive accounting exceeds session policy":
		return "capacity"
	case "login rejected", "negative login result":
		return "login_rejected"
	case "legacy authentication unsupported":
		return "authentication_unsupported"
	case "malformed tunnel frame", "invalid stream event", "invalid internally constructed RDT packet":
		return "invalid_frame"
	case "remote abort", "remote quit without orderly close", "peer stopped during creation", "peer closed with receive gap", "remote exit":
		return "peer_closed"
	case "peer requires DTLS":
		return "dtls_required"
	case "canceled", "creation canceled", "session stopped":
		return "canceled"
	default:
		return "transport_failed"
	}
}

// Channel closes need their own record: they need not fail any session layer.
func (x *stack) channelClosed(a p2ptunnel.Action, c *stream, cause error) {
	if x.f.s.logger == nil || a.Rule != "" {
		return
	}
	reason := layerReason(a.Reason)
	switch a.Reason {
	case "receive budget exceeded":
		reason = "receive_budget"
	case "peer close":
		reason = "peer_close"
	case "both directions ended", "local EOF after peer end":
		reason = "ended"
	case "local close":
		reason = "local_close"
	case "local write failed":
		reason = "local_write_failed"
	case "peer replaced setup":
		reason = "peer_replaced_setup"
	}
	fields := []any{"layer", "tunnel", "channel", int(a.Channel), "reason", reason, "cause", channelCause(cause)}
	if c != nil {
		fields = append(fields, "port", c.port)
	}
	if a.PeerClose != nil {
		fields = append(fields, "peer_has_result", a.PeerClose.HasResult, "peer_result", int(a.PeerClose.Result), "local_end", a.PeerClose.LocalEnd)
	}
	if abnormalChannel(cause) && (a.Reason == "peer close" || a.Reason == "receive budget exceeded" || a.Reason == "local write failed" || a.Reason == "peer replaced setup") {
		x.f.s.logger.Warn("channel_closed", fields...)
	} else {
		x.f.s.logger.Debug("channel_closed", fields...)
	}
}

// relayDiagnostic formats only the core's fixed-size diagnostic value. Action
// envelopes contain sensitive material and must never be logged as a whole.
func (x *stack) relayDiagnostic(message string, p iotc.RelayProgress, failures iotc.FailureReason) {
	l := x.f.s.logger
	if l == nil {
		return
	}
	fields := []any{
		"phase", "rendezvous",
		"relay_phase", p.Phase.String(),
		"relay_requests", p.Requests,
		"relay_pending_results", p.PendingResults,
		"relay_ready_results", p.ReadyResults,
		"relay_refused_results", p.RefusedResults,
		"relay_server_refused_results", p.ServerRefusedResults,
		"relay_refused_paths", p.RefusedPaths,
		"relay_info_requests", p.InfoRequests,
		"relay_info_outstanding", p.InfoOutstanding,
		"relay_info_received", p.InfoReceived,
		"relay_selected", p.Selected,
	}
	if message == "relay_failure_summary" {
		// Derive the trusted reason from typed evidence, never Action.Reason.
		fields = append(fields, "reason", p.Diagnostic(failures))
		l.Info(message, fields...)
	} else {
		l.Info(message, fields...)
	}
}

// channelFailure marks a terminal outcome owned by the channel adapter. The
// bridge sees the same cause through Read and must not report it again.
type channelFailure struct{ error }

func (e *channelFailure) Unwrap() error { return e.error }

func abnormalChannel(err error) bool {
	return err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed)
}

// Mark the local write boundary, rather than classifying by copy direction:
// device_to_local can fail while reading the device or writing the client.
type localClosedError struct{ error }

func (e *localClosedError) Unwrap() error { return e.error }

type localSocketWriter struct{ io.Writer }

func (w localSocketWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	return n, localWriteError(err)
}

func localWriteError(err error) error {
	if oserror.Is(err, syscall.EPIPE) || oserror.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
		return &localClosedError{err}
	}
	return err
}

func channelCause(err error) string {
	var local *localClosedError
	if errors.As(err, &local) {
		return "local_closed"
	}
	if errors.Is(err, ErrSetupRejected) {
		return "setup_rejected"
	}
	var peer *PeerCloseError
	if errors.As(err, &peer) {
		return "peer_io_failed"
	}
	if errors.Is(err, io.EOF) {
		return "ended"
	}
	if errors.Is(err, net.ErrClosed) {
		return "local_close"
	}
	return diagnostic.Class(err)
}
