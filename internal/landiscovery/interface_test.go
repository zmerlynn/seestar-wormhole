package landiscovery

import (
	"net"
	"testing"
)

func TestEligibleInterface(t *testing.T) {
	for _, tc := range []struct {
		name          string
		flags         net.Flags
		broadcastOnly bool
		allInterfaces bool
	}{
		{"down", 0, false, false},
		{"up", net.FlagUp, false, true},
		{"broadcast-down", net.FlagBroadcast, false, false},
		{"broadcast-up", net.FlagUp | net.FlagBroadcast, true, true},
		{"loopback-down", net.FlagLoopback, false, false},
		{"loopback-up", net.FlagUp | net.FlagLoopback, false, false},
		{"loopback-broadcast-down", net.FlagLoopback | net.FlagBroadcast, false, false},
		{"loopback-broadcast-up", net.FlagUp | net.FlagLoopback | net.FlagBroadcast, false, false},
		{"virtual-up", net.FlagUp | net.FlagRunning, false, true},
		{"point-to-point-up", net.FlagUp | net.FlagPointToPoint, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, all := range []bool{false, true} {
				want := tc.broadcastOnly
				if all {
					want = tc.allInterfaces
				}
				if got := eligibleInterface(tc.flags, Options{AllInterfaces: all}); got != want {
					t.Errorf("flags=%v all_interfaces=%t: got %t, want %t", tc.flags, all, got, want)
				}
			}
		})
	}
}
