package iotc

// Direct reports whether an established session selected direct UDP rather than
// a relay. It exposes no address, key or identifier; State gates its validity.
func (m *Machine) Direct() bool { return m.state == Established && m.direct }
