//go:build guitest

package guitest

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestDarkPixelEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	for _, mode := range []string{"blank", "light", "text"} {
		im := image.NewGray(image.Rect(0, 0, 120, 100))
		if mode == "light" {
			for i := range im.Pix {
				im.Pix[i] = 255
			}
		}
		if mode == "text" {
			for x := 45; x < 75; x++ {
				im.SetGray(x, 30, color.Gray{255})
			}
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = png.Encode(f, im); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		err = CheckDarkPixels(path)
		if (err == nil) != (mode == "text") {
			t.Fatalf("%s: %v", mode, err)
		}
	}
}

func TestWaitBound(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Wait(ctx, "missing", func() bool { return false }); err == nil {
		t.Fatal("unbounded wait")
	}
}
