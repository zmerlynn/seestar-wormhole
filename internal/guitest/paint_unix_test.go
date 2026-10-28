//go:build guitest && unix

package guitest

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureRejectsUnpaintedPage(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "fixture.png")
	t.Setenv("PAINT_FIXTURE", source)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"import", "screencapture"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nfor last; do :; done\ncp \"$PAINT_FIXTURE\" \"$last\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, painted := range []bool{false, true} {
		im := image.NewGray(image.Rect(0, 0, 200, 200))
		// Decorations differ, even when the actual page remains completely blank.
		for y := 20; y < 180; y++ {
			for x := 20; x < 180; x++ {
				im.SetGray(x, y, color.Gray{255})
			}
		}
		if painted {
			im.SetGray(100, 80, color.Gray{0})
		}
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
		err = Capture(t.Context(), filepath.Join(dir, "capture.png"), 0)
		if (err == nil) != painted {
			t.Fatalf("painted=%v: %v", painted, err)
		}
	}
}
