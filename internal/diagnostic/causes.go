package diagnostic

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Cause identifies an error node without formatting its potentially private text.
type Cause struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Parent int    `json:"parent"`
}

// Causes preserves the shape and typed reasons of wrapped/joined failures for
// debug logs. Parent indexes distinguish independent siblings. It never
// calls an error's Error, String or LogValue method or reads backend paths.
func Causes(err error) []Cause {
	return walkCauses(err).nodes
}

type causeGraph struct {
	nodes     []Cause
	parents   [64]bool
	truncated bool
}

func walkCauses(err error) causeGraph {
	var graph causeGraph
	var visit func(error, int)
	visit = func(err error, parent int) {
		if err == nil {
			return
		}
		// A retained wrapper is not a leaf even if its child is omitted.
		if parent >= 0 {
			graph.parents[parent] = true
		}
		if len(graph.nodes) >= 64 {
			graph.truncated = true
			return
		}
		reason := Class(err)
		if reason == "operation_failed" {
			switch {
			case err == session.ErrClosed:
				reason = "owner_closed"
			case err == errors.ErrUnsupported:
				reason = "unsupported"
			case errors.Is(err, io.EOF):
				reason = "peer_closed"
			case errors.Is(err, net.ErrClosed):
				reason = "connection_closed"
			}
		}
		index := len(graph.nodes)
		graph.nodes = append(graph.nodes, Cause{Type: fmt.Sprintf("%T", err), Reason: reason, Parent: parent})
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, cause := range wrapped.Unwrap() {
				visit(cause, index)
				if graph.truncated {
					break
				}
			}
		case interface{ Unwrap() error }:
			visit(wrapped.Unwrap(), index)
		}
	}
	visit(err, -1)
	return graph
}

// leafCauses collapses duplicate leaf labels and bounds the default line. The
// bounded graph stays available in the separate DEBUG record. Omitted nodes
// or labels are marked even if all retained leaves share the same label.
func leafCauses(graph causeGraph) []string {
	var leaves []string
	for i, cause := range graph.nodes {
		if graph.parents[i] || slices.Contains(leaves, cause.Reason) {
			continue
		}
		if len(leaves) == 8 {
			return append(leaves, "more")
		}
		leaves = append(leaves, cause.Reason)
	}
	if graph.truncated {
		leaves = append(leaves, "more")
	}
	return leaves
}

func logFinalRecord(l *slog.Logger, message, phase, reason string, err, cleanup error) {
	graph := walkCauses(err)
	l.Debug("diagnostic_causes", "diagnostic", message, "phase", phase, "reason", reason, "causes", graph.nodes)
	attrs := []any{"phase", phase, "reason", reason, "cleanup_reason", CleanupClass(cleanup), "leaf_causes", leafCauses(graph)}
	if reason == "cloud_license_missing" {
		attrs = append(attrs, "hint", licenseMissingHint)
	}

	l.Error(message, attrs...)
}
