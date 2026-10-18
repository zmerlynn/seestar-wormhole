package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestSessionConfigFlagDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, command, flag, value, rule, want string
	}{
		{"idle", "proxy", "--idle", "-17ns", "duration must be nonnegative", "--idle: duration must be nonnegative"},
		{"pause negative", "proxy", "--pause-threshold", "-29", "byte count must be 2..8388608", "--pause-threshold: byte count must be 2..8388608"},
		{"pause zero", "proxy", "--pause-threshold", "0", "byte count must be 2..8388608", "--pause-threshold: byte count must be 2..8388608"},
		{"pause one", "proxy", "--pause-threshold", "1", "byte count must be 2..8388608", "--pause-threshold: byte count must be 2..8388608"},
		{"pause above max", "proxy", "--pause-threshold", "8388609", "byte count must be 2..8388608", "--pause-threshold: byte count must be 2..8388608"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{tt.command, tt.flag + "=" + tt.value}
			var out, logs bytes.Buffer
			err := run(context.Background(), args, &out, &logs)
			var detail *diagnostic.OptionError
			if !errors.As(err, &detail) || detail.Error() != tt.want {
				t.Fatalf("error=%v want OptionError %q", err, tt.want)
			}
			if got := commandExitCode(options{command: commandProxy}, err); got != 2 {
				t.Fatalf("exit=%d want 2", got)
			}
			assertConfigRecord(t, logs.Bytes(), tt.command, tt.flag, tt.rule)
			if tt.command == "proxy" && out.Len() != 0 {
				t.Fatalf("stdout=%q want empty", out.String())
			}
		})
	}
}
