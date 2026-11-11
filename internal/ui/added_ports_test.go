package ui

import (
	"slices"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

// Ports holds standard-service overrides and added ports, under -p's rules.
func TestAddedPortsValidation(t *testing.T) {
	defaults, err := proxyrun.MergePorts(nil)
	if err != nil {
		t.Fatal(err)
	}
	// room is how many added ports fit with every default forwarded.
	room := 128 - len(defaults)
	many := func(n int) []proxyrun.Port {
		ports := make([]proxyrun.Port, n)
		for i := range ports {
			ports[i] = proxyrun.Port{Local: uint16(20000 + i), Device: uint16(20000 + i)}
		}
		return ports
	}
	for _, tc := range []struct {
		name  string
		ports []proxyrun.Port
		valid bool
	}{
		{"added", []proxyrun.Port{{Local: 1445, Device: 445}, {Local: 5000, Device: 5000}, {Local: 22, Device: 22}}, true},
		{"limit", many(room), true},
		{"over the limit", many(room + 1), false},
		{"device twice", []proxyrun.Port{{Local: 5000, Device: 5000}, {Local: 5001, Device: 5000}}, false},
		{"standard service twice", []proxyrun.Port{{Local: 1445, Device: 445}, {Local: 5001, Device: 445}}, false},
		{"no device port", []proxyrun.Port{{Local: 5000, Device: 0}}, false},
		{"local twice", []proxyrun.Port{{Local: 5000, Device: 5000}, {Local: 5000, Device: 5001}}, false},
		{"local of a default", []proxyrun.Port{{Local: 4445, Device: 5000}}, false},
		{"local of an override", []proxyrun.Port{{Local: 5000, Device: 445}, {Local: 5000, Device: 5000}}, false},
		{"local of a turned-off default", []proxyrun.Port{{Local: 0, Device: 445}, {Local: 4445, Device: 5000}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings()
			s.Ports = tc.ports
			ports, _, err := s.validate()
			if !tc.valid {
				if err == nil || actionError(err).Code != "ports" || actionError(err).Field != "ports" {
					t.Fatalf("validate = %v, %v", ports, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.ports {
				if p.Local != 0 && !slices.ContainsFunc(ports, func(q proxyrun.Port) bool { return q.Local == p.Local && q.Device == p.Device }) {
					t.Fatalf("%v not forwarded: %v", p, ports)
				}
			}
		})
	}
}
