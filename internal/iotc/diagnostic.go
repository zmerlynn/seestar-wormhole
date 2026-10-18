package iotc

// Oddity is a fixed vocabulary for safely tolerated receive input.
type Oddity byte

const (
	LayoutOddity Oddity = iota
	UnknownRecord
	UnknownControl
	OpaqueRoute
	AlertTrigger
	UnknownAlert
	UnknownRelayResult
	UnknownMasterUnit
)

// String returns only fixed category names, never server-provided strings.
func (o Oddity) String() string {
	switch o {
	case LayoutOddity:
		return "layout"
	case UnknownRecord:
		return "unknown_record"
	case UnknownControl:
		return "unknown_control"
	case OpaqueRoute:
		return "opaque_route"
	case AlertTrigger:
		return "alert_trigger"
	case UnknownAlert:
		return "unknown_alert"
	case UnknownRelayResult:
		return "unknown_relay_result"
	case UnknownMasterUnit:
		return "unknown_master_unit"
	default:
		return "other"
	}
}

// Direct reports whether an established session selected direct UDP rather than
// a relay. It exposes no address, key or identifier; State gates its validity.
func (m *Machine) Direct() bool { return m.state == Established && m.direct }

// layoutOddity reports only an admitted layout's opcode. The runtime applies
// its shared debug budget; no packet bytes or identity leave the core.
func (m *Machine) layoutOddity(op uint16, unusual bool) {
	if unusual {
		m.receiveOddity(LayoutOddity, op, 0)
	}
}

func (m *Machine) receiveOddity(category Oddity, op uint16, typ byte) {
	m.emit(Action{Kind: ReceiveOddity, Oddity: category, Opcode: op, Type: typ})
}

func (m *Machine) routeOddity(p Packet) {
	want := uint16(0x12)
	if !m.direct {
		want = 0x24
	}
	if p.Route != want {
		m.receiveOddity(OpaqueRoute, p.Opcode, 0)
	}
}
