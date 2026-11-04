package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// A temporary file is named tempPrefix, then the 64 lowercase hex digits of
// a SHA-256, then tempSuffix, in the folder of the file it becomes.
const (
	tempPrefix  = ".seestar-copy-"
	tempSuffix  = ".tmp"
	tempHexLen  = 64
	tempNameLen = len(tempPrefix) + tempHexLen + len(tempSuffix)
)

// TempName returns the base name of the temporary file of share path p,
// from the SHA-256 of p: a fixed length whatever p is, a leading dot, and
// no .fit ending.
func TempName(p string) string {
	sum := sha256.Sum256([]byte(p))
	return tempPrefix + hex.EncodeToString(sum[:]) + tempSuffix
}

// IsTempName reports whether the base name name is exactly the name of a
// temporary file: tempPrefix, 64 lowercase hex digits and tempSuffix.
func IsTempName(name string) bool {
	if len(name) != tempNameLen || !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, tempSuffix) {
		return false
	}
	for _, c := range []byte(name[len(tempPrefix) : len(tempPrefix)+tempHexLen]) {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// checkPath returns the rule a listed path breaks, or "" if the path can be
// stored as <dest>/<path>. Device listings are untrusted input, so a path
// must be relative, use forward slashes only, and be clean: every accepted
// path is its own path.Clean. With windows set, it must also be a name
// Windows can store.
func checkPath(p string, windows bool) string {
	switch {
	case p == "":
		return "empty path"
	case !utf8.ValidString(p):
		// The state file is JSON, which cannot carry the path byte for byte.
		return "path is not valid UTF-8"
	case strings.HasPrefix(p, "/"):
		return "absolute path"
	case strings.Contains(p, `\`):
		return "backslash in path"
	case hasDriveLetter(p):
		return "path starts with a drive letter"
	}
	if strings.IndexByte(p, 0) >= 0 {
		// No platform can store NUL in a name.
		return "control character U+0000 in path"
	}
	if windows {
		// Other control characters are allowed except by Windows, which
		// rejects U+0001 to U+001F. DEL and U+0080 to U+009F are allowed
		// everywhere. In valid UTF-8, a byte below 0x20 is always one of
		// these characters.
		if i := strings.IndexFunc(p, func(r rune) bool { return r < 0x20 }); i >= 0 {
			return fmt.Sprintf("control character %U is not allowed on Windows", p[i])
		}
		if i := strings.IndexAny(p, `:*?"<>|`); i >= 0 {
			return fmt.Sprintf("character %q is not allowed on Windows", p[i])
		}
	}
	elems := strings.Split(p, "/")
	last := len(elems) - 1
	for i, elem := range elems {
		switch {
		case elem == "":
			return "empty path element"
		case elem == "." || elem == "..":
			return fmt.Sprintf("path element %q", elem)
		case i == last && IsTempName(strings.ToLower(elem)):
			// Such a file would be taken for a leftover temporary file
			// and removed at startup. Case-insensitive: Windows and macOS
			// folders usually are.
			return fmt.Sprintf("name %q is reserved for temporary files", elem)
		case !windows:
		case strings.HasSuffix(elem, "."):
			return fmt.Sprintf("name %q ends in a dot, which Windows cannot store", elem)
		case strings.HasSuffix(elem, " "):
			return fmt.Sprintf("name %q ends in a space, which Windows cannot store", elem)
		case reservedOnWindows(elem):
			return fmt.Sprintf("name %q is a reserved device name on Windows", elem)
		}
	}
	return ""
}

// hasDriveLetter reports whether p starts like a Windows volume, such as "C:".
func hasDriveLetter(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0] | 0x20 // ASCII lower case
	return 'a' <= c && c <= 'z'
}

// reservedOnWindows reports whether a path element names a Windows device:
// CON, PRN, AUX, NUL, CONIN$, CONOUT$, COM0-9 and LPT0-9 (including the
// superscript digits ¹ ² ³), in any case, alone or with an extension. Windows
// ignores trailing spaces before the extension, so "CON .txt" is reserved too.
func reservedOnWindows(elem string) bool {
	base, _, _ := strings.Cut(elem, ".")
	base = strings.TrimRight(base, " ")
	for _, name := range []string{"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$"} {
		if strings.EqualFold(base, name) {
			return true
		}
	}
	if len(base) < 4 || !strings.EqualFold(base[:3], "COM") && !strings.EqualFold(base[:3], "LPT") {
		return false
	}
	switch digit := base[3:]; digit {
	case "¹", "²", "³":
		return true
	default:
		return len(digit) == 1 && '0' <= digit[0] && digit[0] <= '9'
	}
}
