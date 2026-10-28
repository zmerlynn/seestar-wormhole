// Package icon supplies the application's embedded mark and native window icon.
package icon

import _ "embed"

//go:embed icon.svg
var SVG []byte

//go:embed icon-256.png
var PNG []byte
