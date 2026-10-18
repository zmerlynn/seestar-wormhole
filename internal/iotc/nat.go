package iotc

import "net/netip"

// NATTracker classifies observed address changes and connection state.
// The integer is an observation code, not a claim of a complete RFC NAT taxonomy.
// It is attempt-local; the device's multi-session 25-second refresh does not apply.
type NATTracker struct {
	records [8]struct{ server, observed netip.AddrPort }
	count   int
	other   bool
}

// Observe records a correlated hello response, clearing observations if the same
// server reports a changed mapping. The caller validates serial/instance/source.
func (n *NATTracker) Observe(server, observed netip.AddrPort, other bool) {
	if !server.IsValid() || !observed.IsValid() {
		return
	}
	for i := 0; i < n.count; i++ {
		r := n.records[i]
		if r.server == server {
			if r.observed == observed {
				n.other = n.other || other
				return
			}
			*n = NATTracker{}
			break
		}
	}
	if n.count < 8 {
		n.records[n.count].server = server
		n.records[n.count].observed = observed
		n.count++
	}
	n.other = n.other || other
}

// Type returns 1 for alternate-source reception, 0 for insufficient observations,
// 2 for a shared mapping across servers, or 3 for differing mappings.
// Observation codes follow the native tracker, without inventing RFC NAT names:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#nat-classification
func (n *NATTracker) Type() byte {
	if n.other {
		return 1
	}
	if n.count < 2 {
		return 0
	}
	for i := 0; i < n.count; i++ {
		for j := 0; j < i; j++ {
			if n.records[i].observed == n.records[j].observed {
				return 2
			}
		}
	}
	return 3
}
