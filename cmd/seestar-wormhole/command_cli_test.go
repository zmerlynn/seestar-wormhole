//go:build !gui

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCLIBareInvocationPrintsHelpWithoutBootstrap(t *testing.T) {
	for _, args := range [][]string{nil, {"--version=false"}} {
		var out, logs bytes.Buffer
		code := mainExitCodeWithRunner(args, &out, &logs,
			func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				t.Fatal("help acquired signals")
				return nil, nil
			},
			func() (io.Writer, func()) { t.Fatal("help acquired logs"); return nil, nil }, false,
			func(context.Context, options, io.Writer, io.Writer) error {
				t.Fatal("help entered runtime")
				return nil
			})
		if code != 0 || !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "seestar-wormhole-cli") || logs.Len() != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, &out, &logs)
		}
	}
}

func TestCLIHasNoUICommand(t *testing.T) {
	var o options
	for _, cmd := range newCommand(&o, io.Discard).Commands() {
		if cmd.Name() == "ui" {
			t.Fatal("CLI exposes ui")
		}
	}
	for _, args := range [][]string{{"ui"}, {"ui", "--help"}} {
		if _, err := parse(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
