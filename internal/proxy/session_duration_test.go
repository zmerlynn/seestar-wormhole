package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestSessionConfigDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, option, rule, want string
		cfg                      Config
	}{
		{"Idle", "--idle", "duration must be nonnegative", "--idle: duration must be nonnegative", Config{Idle: -time.Nanosecond}},
		{"StatsInterval", "--stats-interval", "duration must be nonnegative", "--stats-interval: duration must be nonnegative", Config{StatsInterval: -time.Nanosecond}},
		{"BusyWait", "--busy-wait", "use a nonnegative Go duration", "--busy-wait: use a nonnegative Go duration", Config{BusyWait: -time.Nanosecond}},
		{"PauseThreshold/negative", "--pause-threshold", "byte count must be 2..8388608 (zero selects default)", "--pause-threshold: byte count must be 2..8388608 (zero selects default)", Config{PauseThreshold: -1}},
		{"PauseThreshold/one", "--pause-threshold", "byte count must be 2..8388608 (zero selects default)", "--pause-threshold: byte count must be 2..8388608 (zero selects default)", Config{PauseThreshold: 1}},
		{"PauseThreshold/above_max", "--pause-threshold", "byte count must be 2..8388608 (zero selects default)", "--pause-threshold: byte count must be 2..8388608 (zero selects default)", Config{PauseThreshold: 8388609}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			s, err := start(context.Background(), tt.cfg, func(*Session) sessionedge.Factory {
				called = true
				return nil
			})
			if s != nil {
				s.Close()
				t.Fatal("invalid configuration returned a session")
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error=%v want %q", err, tt.want)
			}
			var detail *diagnostic.OptionError
			if !errors.As(err, &detail) || detail.Option != tt.option || detail.Rule != tt.rule {
				t.Fatalf("error=%#v want OptionError{%q, %q}", err, tt.option, tt.rule)
			}
			if called {
				t.Fatal("invalid configuration reached factory")
			}
		})
	}
}

func TestSessionConfigAccepted(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
	}{
		{"zero_defaults", Config{}},
		{"positive_durations", Config{Idle: time.Second, StatsInterval: time.Nanosecond, BusyWait: time.Second}},
		{"hold_zero_busy_wait", Config{HoldOnDemand: true}},
		{"pause_min", Config{PauseThreshold: 2}},
		{"pause_max", Config{PauseThreshold: 8388608}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, _, _ := workloadSession(t, t.Context(), func(*simulatedWire) {}, tt.cfg)
				if err := s.Close(); err != nil {
					t.Fatalf("accepted configuration close: %v", err)
				}
			})
		})
	}
}
