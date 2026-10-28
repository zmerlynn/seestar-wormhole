// Package icon supplies the application's embedded mark and native window icon.
// Regenerate the icon-only Windows resources from the repository root:
//
// go run github.com/tc-hib/go-winres@v0.3.3 simply --icon internal/icon/icon-256.png --manifest none --arch amd64,arm64 --out cmd/seestar-wormhole/rsrc
//
// The generator is a build tool; the resources contain only this project's icon.
package icon

import _ "embed"

//go:embed icon.svg
var SVG []byte

//go:embed icon-256.png
var PNG []byte
