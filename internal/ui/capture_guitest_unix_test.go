//go:build gui && guitest && unix

package ui

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
)

func TestFailureCaptureAfterDriverDeadline(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "painted.png")
	im := image.NewGray(image.Rect(0, 0, 200, 200))
	im.SetGray(100, 80, color.Gray{255})
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAINT_FIXTURE", source)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"import", "screencapture"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nfor last; do :; done\ncp \"$PAINT_FIXTURE\" \"$last\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// No running app or JS bridge: a failure capture must use only its own
	// fresh deadline and the native screenshot path.
	d := &guiTestDriver{ctx: ctx, w: &application.WebviewWindow{}}
	path := filepath.Join(dir, "failure.png")
	if err := d.capture(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := guitest.CheckPainted(path); err != nil {
		t.Fatal(err)
	}
}
