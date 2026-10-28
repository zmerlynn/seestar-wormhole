//go:build guitest

// Package guitest contains only native screenshot and bounded-wait helpers for
// the offline GUI fixture. It is absent from ordinary application builds.
package guitest

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func Wait(ctx context.Context, why string, predicate func() bool) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if predicate() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", why, ctx.Err())
		case <-tick.C:
		}
	}
}

func Capture(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "screencapture", "-x", path)
	case "linux":
		cmd = exec.CommandContext(ctx, "import", "-window", "root", path)
	case "windows":
		// Screen capture only; no accessibility or UI automation API.
		script := `Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$i = New-Object System.Drawing.Bitmap $b.Width, $b.Height
$g = [System.Drawing.Graphics]::FromImage($i)
try { $g.CopyFromScreen($b.Location, [System.Drawing.Point]::Empty, $b.Size); $i.Save('` + strings.ReplaceAll(path, "'", "''") + `', [System.Drawing.Imaging.ImageFormat]::Png) } finally { $g.Dispose(); $i.Dispose() }`
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	default:
		return fmt.Errorf("unsupported screenshot platform")
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("screenshot: %w: %s", err, b)
	}
	return nil
}

// CheckDarkPixels is deliberately coarse: require substantial dark background
// and light glyph pixels in the central page region, excluding screen edges.
// The driver separately checks the actual DOM text and computed color contrast.
func CheckDarkPixels(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		return err
	}
	b := im.Bounds()
	dark, light, total := 0, 0, 0
	for y := b.Min.Y + b.Dy()/5; y < b.Min.Y+b.Dy()*3/5; y++ {
		for x := b.Min.X + b.Dx()/3; x < b.Min.X+b.Dx()*2/3; x++ {
			r, g, bl, _ := im.At(x, y).RGBA()
			l := (2126*uint64(r) + 7152*uint64(g) + 722*uint64(bl)) / 10000
			total++
			if l < 16384 {
				dark++
			}
			if l > 49151 {
				light++
			}
		}
	}
	if total == 0 || dark < total/2 || light < 20 {
		return fmt.Errorf("dark screenshot contrast: dark=%d light=%d total=%d", dark, light, total)
	}
	return nil
}
