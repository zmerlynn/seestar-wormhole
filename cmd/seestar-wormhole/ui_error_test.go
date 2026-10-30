package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func TestUIStartupErrorReporter(t *testing.T) {
	saved := reportUIError
	defer func() { reportUIError = saved }()
	for _, args := range [][]string{{"ui"}, {"ui", "--unknown"}, {"proxy"}} {
		calls := 0
		reportUIError = func(error) { calls++ }
		mainExitCodeWithRunner(args, io.Discard, io.Discard,
			func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
			func() (io.Writer, func()) { return io.Discard, func() {} }, true,
			func(context.Context, options, io.Writer, io.Writer) error { return errors.New("startup failed") })
		want := 1
		if args[0] == "proxy" {
			want = 0
		}
		if calls != want {
			t.Fatalf("%q: startup notifications %d, want %d", args, calls, want)
		}
	}
}
