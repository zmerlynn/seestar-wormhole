package iotc

import "fmt"

// RelayPhase is the furthest observed relay milestone in this attempt. It never
// regresses when a competitor refuses or a link retires; it is not wire state.
type RelayPhase byte

const (
	RelayNotStarted RelayPhase = iota
	RelayRequesting
	RelayPending
	RelayAccepted
	RelayInfoOutstanding
	RelayInfoReceived
	RelaySelected
)

// String returns a fixed label, including for unknown local values.
func (p RelayPhase) String() string {
	switch p {
	case RelayNotStarted:
		return "not_started"
	case RelayRequesting:
		return "requesting"
	case RelayPending:
		return "pending"
	case RelayAccepted:
		return "accepted"
	case RelayInfoOutstanding:
		return "info_outstanding"
	case RelayInfoReceived:
		return "info_received"
	case RelaySelected:
		return "selected"
	default:
		return "unknown"
	}
}

// RelayProgress is a secret-free, constant-size attempt summary. Cumulative
// counters saturate at 65535, including duplicate authenticated results. Requests
// count Send actions handed to the edge, not guaranteed delivery. Outstanding
// counts live paths with info requested but not received, never queued packets.
// Results follow the relay result space; milestones observe existing
// selection and cleanup without authorizing any protocol transition.
type RelayProgress struct {
	Phase                                                  RelayPhase
	Requests, PendingResults, ReadyResults, RefusedResults uint16
	ServerRefusedResults, RefusedPaths                     uint16
	InfoRequests, InfoOutstanding, InfoReceived            uint16
	Selected                                               bool
}

func increment(n *uint16) {
	if *n < 65535 {
		*n++
	}
}

func (m *Machine) relayPhase(phase RelayPhase) {
	m.progress.Phase = max(m.progress.Phase, phase)
}

// RelayProgress returns current bounded counters, retaining the final snapshot
// after cleanup. It contains no endpoint, session identity or peer data.
func (m *Machine) RelayProgress() RelayProgress {
	p := m.progress
	if m.state != Closed {
		p.InfoOutstanding = 0
		for _, v := range m.endpoints[:m.count] {
			if v.infoRequested && v.stage == relayInfoPending && v.connected {
				increment(&p.InfoOutstanding)
			}
		}
	}
	return p
}

func (m *Machine) relaySend(p operation) {
	switch p.opcode {
	case 0x020c:
		increment(&m.progress.Requests)
		m.relayPhase(RelayRequesting)
	case 0x0241:
		increment(&m.progress.InfoRequests)
		if i := m.find(p.link); i >= 0 {
			m.endpoints[i].infoRequested = true
		}
		m.relayPhase(RelayInfoOutstanding)
	}
}

// observeRelayResult runs only after envelope authentication, UID, SID and RID
// checks. Late/duplicate replies count as evidence but do not change wire state.
func (m *Machine) observeRelayResult(v *endpoint, result int32) {
	switch result {
	case -7:
		increment(&m.progress.PendingResults)
		if v.stage == rendezvousPending {
			m.relayPhase(RelayPending)
		}
	case 0:
		increment(&m.progress.ReadyResults)
	default:
		if relayFailure(result) == 0 {
			return // An unknown enum provides no evidence of a known outcome.
		}
		increment(&m.progress.RefusedResults)
		if result == -2 {
			increment(&m.progress.ServerRefusedResults)
		}
		if !v.relayRefusalObserved {
			v.relayRefusalObserved = true
			increment(&m.progress.RefusedPaths)
		}
	}
}

// TimeoutReason names the furthest stalled stage using only bounded local
// evidence. Competing refusal bits remain separate on the Failed action.
func (p RelayProgress) TimeoutReason() string {
	switch p.Phase {
	case RelayRequesting:
		if p.RefusedResults != 0 {
			return fmt.Sprintf("relay requested; no usable result after %d requests", p.Requests)
		}
		return fmt.Sprintf("relay result never arrived after %d requests", p.Requests)
	case RelayPending:
		return fmt.Sprintf("relay pending; relay never became ready after %d requests", p.Requests)
	case RelayAccepted:
		return "relay accepted; relay info request never sent"
	case RelayInfoOutstanding:
		return fmt.Sprintf("relay accepted; relay info never arrived after %d requests", p.InfoRequests)
	case RelayInfoReceived:
		return "relay info received; relay selection did not complete"
	default:
		return ""
	}
}

// Diagnostic returns a trusted terminal summary without formatting Action.Reason
// or any peer-supplied value. Refusal evidence is preserved alongside the stall.
func (p RelayProgress) Diagnostic(reasons FailureReason) string {
	reason := reasons.String()
	if reasons&ConnectionTimeout != 0 {
		if stalled := p.TimeoutReason(); stalled != "" {
			reason = stalled + "; " + reason
		}
	}
	if reason == "" {
		return "transport_failed"
	}
	return reason
}
