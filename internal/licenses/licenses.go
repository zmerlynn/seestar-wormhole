// Package licenses prints the notices for third-party software in this build.
package licenses

import (
	"bytes"
	"embed"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"runtime/debug"
)

// The catalog includes platform-specific dependencies so the same source tree
// can be cross-built. Build metadata selects only this executable's modules.
//
//go:embed embed
var notices embed.FS

type module struct {
	Path  string
	SPDX  string
	Files []string
}

// Write prints the project NOTICE followed by each linked module's full texts.
func Write(out io.Writer) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("licenses: Go build information is unavailable")
	}
	manifest, err := notices.ReadFile("embed/modules.json")
	if err != nil {
		return err
	}
	var entries []module
	if err := json.Unmarshal(manifest, &entries); err != nil {
		return err
	}
	linked := make(map[string]bool, len(info.Deps))
	for _, dep := range info.Deps {
		linked[dep.Path] = true
	}
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		known[entry.Path] = true
	}
	for path := range linked {
		if !known[path] {
			return fmt.Errorf("licenses: no embedded license for linked module %s", path)
		}
	}
	if err := writeText(out, "NOTICE"); err != nil {
		return err
	}
	for _, entry := range entries {
		if !linked[entry.Path] {
			continue
		}
		if _, err := fmt.Fprintf(out, "\nModule: %s\nSPDX: %s\n", entry.Path, entry.SPDX); err != nil {
			return err
		}
		for _, path := range entry.Files {
			if _, err := fmt.Fprintf(out, "\n--- %s ---\n", path); err != nil {
				return err
			}
			if err := writeText(out, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeText(out io.Writer, path string) error {
	data, err := notices.ReadFile("embed/" + path)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		return err
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		_, err = fmt.Fprintln(out)
	}
	return err
}
