package iotc

import "strings"

// FailureReason is a bounded set of terminal path outcomes on a Failed action.
// Multiple bits preserve disagreements between competing servers. In particular,
// AnotherRelayRequest does not establish cloud-level device/session ownership.
type FailureReason uint32

// Terminal reasons distinguish relay authorization/allocation failures from
// alert plaintext and UDP/TCP early exits.
const (
	DeviceOffline FailureReason = 1 << iota
	DeviceAsleep
	DeviceRefused
	ServerRefused
	DeviceUnknown
	AnotherRelayRequest
	RelayUnsupported
	MultipleDeviceLogins
	PortRejected
	AuthenticationRejected
	AuthenticationUnsupported
	RelayRejected
	TransportFailure
	ConnectionTimeout
)

// String returns fixed, secret-free descriptions in stable order.
func (r FailureReason) String() string {
	labels := [...]string{"device offline", "device asleep", "device refused", "server refused", "device unknown", "another relay request", "relay unsupported", "multiple device logins", "port rejected", "authentication rejected", "authentication unsupported", "relay rejected", "transport failed", "connection timeout"}
	var reasons []string
	for i, label := range labels {
		if r&(1<<i) != 0 {
			reasons = append(reasons, label)
		}
	}
	return strings.Join(reasons, "; ")
}

func relayFailure(result int32) FailureReason {
	switch result {
	case -1:
		return DeviceRefused
	case -2:
		return ServerRefused
	case -3:
		return DeviceUnknown
	case -4:
		return AnotherRelayRequest
	case -5:
		return RelayUnsupported
	case -6:
		return MultipleDeviceLogins
	case -8:
		return DeviceAsleep
	case -9:
		return PortRejected
	case -22:
		return AuthenticationRejected
	case -23:
		return AuthenticationUnsupported
	default:
		return 0 // Unknown is neither authorization nor a known refusal.
	}
}

func (m *Machine) terminalEndpoint(i int, reason FailureReason) {
	m.retireEndpoint(i)
	m.endpoints[i].stage = endpointTerminal
	m.endpoints[i].failure = reason
}

func (m *Machine) pathFailures() FailureReason {
	var reasons FailureReason
	for _, v := range m.endpoints[:m.count] {
		reasons |= v.failure
	}
	return reasons
}

func (m *Machine) failConnecting(reasons FailureReason) {
	m.close(m.RelayProgress().Diagnostic(reasons), false)
	// close emits exactly one upward Failed and clears the path table. Carry the
	// bounded reasons on that action, not identifiers or server-supplied strings.
	m.out[len(m.out)-1].Failures = reasons
}

func (m *Machine) finishIfExhausted() {
	if m.state != Connecting || m.count == 0 || m.resolveToken != 0 || m.materialToken != 0 || m.hasLiveCandidates() {
		return
	}
	for _, v := range m.endpoints[:m.count] {
		if v.stage < endpointTerminal && (!v.master || v.stage == endpointInitial) {
			return
		}
	}
	reasons := m.pathFailures()
	if reasons != 0 {
		m.failConnecting(reasons)
	}
}

// directPending describes the independent 0240/030d branch. A relay result
// cannot revoke it while selection proceeds in parallel with relay results.
func (v endpoint) directPending() bool {
	return !v.directExpired && !v.master && v.connected && v.link.Transport == UDP && v.peerNAT != 10 && v.stage >= rendezvousPending && v.stage <= relayRefused
}

func (m *Machine) refuseRelay(i int, reason FailureReason) {
	v := &m.endpoints[i]
	if !v.directPending() {
		m.terminalEndpoint(i, reason)
		return
	}
	v.stage, v.failure = relayRefused, reason
	// Keep incoming authentication and direct request completions. Only relay
	// sends lose authority; clearing the whole link would discard a delayed punch.
	for j, p := range m.pending {
		if p.link == v.link && p.kind == Seal && (p.opcode == 0x020c || p.opcode == 0x0241 || p.opcode == 0x030f) {
			m.pending[j] = operation{}
		}
	}
}
