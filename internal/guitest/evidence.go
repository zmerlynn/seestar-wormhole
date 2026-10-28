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
	"strconv"
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

func Capture(ctx context.Context, path string, window uintptr) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "screencapture", "-x", path)
	case "linux":
		cmd = exec.CommandContext(ctx, "import", "-window", "root", path)
	case "windows":
		// Foreground ownership is asserted before copying only the app rectangle.
		// A console or another window must never satisfy the pixel assertions.
		script := `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;
using System.Runtime.InteropServices;
public class ShotWindow {
 [StructLayout(LayoutKind.Sequential)] public struct Rect { public int Left, Top, Right, Bottom; }
 [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr w);
 [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
 [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr w, out Rect r);
}
'@
$w=[IntPtr]` + strconv.FormatUint(uint64(window), 10) + `
$deadline=[DateTime]::UtcNow.AddSeconds(2)
do {
 [ShotWindow]::SetForegroundWindow($w) | Out-Null
 Start-Sleep -Milliseconds 50
} until ([ShotWindow]::GetForegroundWindow() -eq $w -or [DateTime]::UtcNow -ge $deadline)
if ($w -eq [IntPtr]::Zero -or [ShotWindow]::GetForegroundWindow() -ne $w) { throw 'App is not foreground for screenshot' }
$r=New-Object ShotWindow+Rect
if (-not [ShotWindow]::GetWindowRect($w,[ref]$r)) { throw 'App rectangle unavailable' }
$i = New-Object System.Drawing.Bitmap ($r.Right-$r.Left), ($r.Bottom-$r.Top)
$g = [System.Drawing.Graphics]::FromImage($i)
try { $g.CopyFromScreen($r.Left,$r.Top,0,0,$i.Size); $i.Save('` + strings.ReplaceAll(path, "'", "''") + `', [System.Drawing.Imaging.ImageFormat]::Png) } finally { $g.Dispose(); $i.Dispose() }`
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
