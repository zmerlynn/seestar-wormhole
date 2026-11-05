// Package buildinfo names the build a program is: the version and revision a
// release links into its main package, or, in a development build, the
// build's VCS stamp.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

const development = "development"

// Version returns linked, the version a release build set at link time, or
// "development" when it is empty.
func Version(linked string) string {
	if linked == "" {
		return development
	}
	return linked
}

// Revision returns linked, the revision a release build set at link time.
// When it is empty, it returns the build's VCS stamp, shortened to 12
// characters as release builds do, or "development" without one.
func Revision(linked string) string {
	info, _ := debug.ReadBuildInfo()
	return revision(linked, info)
}

// String prints "<program> <version> (<revision>, <os>/<arch>)" from the
// linked version and revision, as [Version] and [Revision] fill them in.
func String(program, version, revision string) string {
	return format(program, Version(version), Revision(revision), runtime.GOOS, runtime.GOARCH)
}

func revision(linked string, info *debug.BuildInfo) string {
	if linked != "" {
		return linked
	}
	if info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value[:min(len(setting.Value), 12)]
			}
		}
	}
	return development
}

func format(program, version, revision, goos, goarch string) string {
	return program + " " + version + " (" + revision + ", " + goos + "/" + goarch + ")"
}
