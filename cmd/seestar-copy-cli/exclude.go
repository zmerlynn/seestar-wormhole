package main

import (
	"path"
	"strings"
)

// defaultExcludes are the built-in exclude patterns. Each one is checked and
// matched exactly as the same --exclude flag is, and the help shows them as
// those flags.
var defaultExcludes = []string{
	".*",                        // dot names: .DS_Store, .StackReplayTemp, ._*
	"*.tmp",                     // unfinished files, such as a video being written
	"Thumbs.db",                 // Windows thumbnail caches
	"desktop.ini",               // Windows folder settings
	"$RECYCLE.BIN",              // the Windows recycle bin
	"System Volume Information", // Windows volume data
	"/log",                      // the telescope's logs
	"/tmp_stitch",               // the telescope's scratch folders
	"/batch_stack_tmp",
}

// exclude is one checked --exclude pattern.
type exclude struct {
	// flag is the pattern as given, for logs.
	flag string
	// pattern is lowercased and has no leading slash. If anchored, it is
	// matched against the whole share path; otherwise against the last name.
	pattern  string
	anchored bool
}

// parseExclude checks an --exclude pattern. A pattern with no slash matches
// a name at any depth; one with a slash anywhere is a path from the share
// root, with one leading slash allowed. Only literal characters, * and ?
// are allowed, so path.Match never sees a class, an escape or a bad pattern.
// Nothing is cleaned: a pattern that would need it is refused.
func parseExclude(flag string) (exclude, error) {
	bad := func(rule string) (exclude, error) { return exclude{}, &usageError{Option: "--exclude", Rule: rule} }
	switch {
	case strings.ContainsAny(flag, `[\`):
		return bad(`[ and \ are not supported; use * and ?, and / between folders`)
	case strings.Contains(flag, "**"):
		return bad("** is not supported; * matches within one name")
	case len(flag) > 1 && strings.HasSuffix(flag, "/"):
		return bad("omit the trailing /; a pattern matches files and folders")
	}
	p := strings.TrimPrefix(flag, "/")
	for elem := range strings.SplitSeq(p, "/") {
		if elem == "" || elem == "." || elem == ".." {
			return bad("use a name, or a path from the share root, with no empty, . or .. parts")
		}
	}
	return exclude{flag: flag, pattern: strings.ToLower(p), anchored: strings.Contains(flag, "/")}, nil
}

// match reports whether the share path p matches, ignoring case by
// lowercasing both sides; there is no Unicode normalization.
func (e exclude) match(p string) bool {
	if !e.anchored {
		p = path.Base(p)
	}
	ok, _ := path.Match(e.pattern, strings.ToLower(p))
	return ok
}

// parseExcludes returns the built-in patterns, unless noDefaults, and then
// the given ones, all checked.
func parseExcludes(flags []string, noDefaults bool) ([]exclude, error) {
	all := flags
	if !noDefaults {
		all = append(append([]string{}, defaultExcludes...), flags...)
	}
	excludes := make([]exclude, 0, len(all))
	for _, f := range all {
		e, err := parseExclude(f)
		if err != nil {
			return nil, err
		}
		excludes = append(excludes, e)
	}
	return excludes, nil
}

// excluded returns the first pattern that matches p, if any.
func excluded(excludes []exclude, p string) (exclude, bool) {
	for _, e := range excludes {
		if e.match(p) {
			return e, true
		}
	}
	return exclude{}, false
}
