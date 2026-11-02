package main

import (
	"context"
	"path"
	"strings"

	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-copy/internal/remote"
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

// selectionFlags are the flags that choose which part of the share a
// command works on. Every such command registers them with
// [selectionFlags.register] and checks them with [selectionFlags.parse], so
// they mean the same everywhere.
type selectionFlags struct {
	excludes          []string
	noDefaultExcludes bool
	includes          []string
}

func (f *selectionFlags) register(fs *pflag.FlagSet) {
	fs.StringArrayVar(&f.excludes, "exclude", nil, "skip files and folders that match this pattern, as well as the built-in ones; repeatable")
	fs.BoolVar(&f.noDefaultExcludes, "no-default-excludes", false, "do not skip what the built-in patterns match")
	fs.StringArrayVar(&f.includes, "include", nil, "keep only files that match this pattern or are in a folder that does; excludes still win; repeatable")
}

// parse checks the flags, without touching the network or the disk.
func (f selectionFlags) parse() (selector, error) {
	all := f.excludes
	if !f.noDefaultExcludes {
		all = append(append([]string{}, defaultExcludes...), f.excludes...)
	}
	var s selector
	for _, flag := range all {
		p, err := parsePattern("--exclude", flag)
		if err != nil {
			return selector{}, err
		}
		s.excludes = append(s.excludes, p)
	}
	for _, flag := range f.includes {
		p, err := parsePattern("--include", flag)
		if err != nil {
			return selector{}, err
		}
		s.includes = append(s.includes, p)
	}
	return s, nil
}

// selector is a checked selection: what a walk of the share leaves out.
// An entry that an exclude matches is skipped, whatever the includes say.
// With includes, a file is kept only if one of them matches it or a folder
// above it, and a folder is listed only if one of them could match it or
// something below it.
type selector struct {
	excludes []pattern
	includes []pattern
}

// leftOut is what a walk left out.
type leftOut struct {
	// skipped are the entries an exclude matched, in listing order.
	skipped []skippedEntry
	// unmatched counts the entries that no include could select and that
	// no exclude matched. A folder counts once; what is in it is not listed.
	unmatched int
}

// skippedEntry is a listed entry that an exclude matched.
type skippedEntry struct {
	path string
	// by is the first exclude that matched.
	by pattern
}

// walk lists the share over sess, leaving out what the selection does, and
// returns what it kept and what it left out. A folder left out is never
// read. Any listing failure, of the root or of a folder that is kept, fails
// the walk; what was left out before it is still returned.
func (s selector) walk(ctx context.Context, sess remote.Session) ([]remote.Entry, leftOut, error) {
	var out leftOut
	entries, err := sess.Walk(ctx, func(p string, dir bool) bool {
		for _, e := range s.excludes {
			if e.match(p) {
				out.skipped = append(out.skipped, skippedEntry{p, e})
				return true
			}
		}
		if !s.included(p, dir) {
			out.unmatched++
			return true
		}
		return false
	})
	if err != nil {
		return nil, out, err
	}
	return entries, out, nil
}

// included reports whether the includes keep p: with none, everything; a
// file, if one matches it or a folder above it; a folder, if one could
// match it or something below it.
func (s selector) included(p string, dir bool) bool {
	if len(s.includes) == 0 {
		return true
	}
	for _, m := range s.includes {
		if dir && m.mayMatchBelow(p) || !dir && m.matchWithin(p) {
			return true
		}
	}
	return false
}

// pattern is one checked selection pattern.
type pattern struct {
	// flag is the pattern as given, for logs.
	flag string
	// pattern is lowercased and has no leading slash. If anchored, it is
	// matched against the whole share path; otherwise against the last name.
	pattern  string
	anchored bool
}

// parsePattern checks the value of option, a pattern flag. A pattern with
// no slash matches a name at any depth; one with a slash anywhere is a path
// from the share root, with one leading slash allowed. Only literal
// characters, * and ? are allowed, so path.Match never sees a class, an
// escape or a bad pattern. Nothing is cleaned: a pattern that would need it
// is refused.
func parsePattern(option, flag string) (pattern, error) {
	bad := func(rule string) (pattern, error) { return pattern{}, &usageError{Option: option, Rule: rule} }
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
	return pattern{flag: flag, pattern: strings.ToLower(p), anchored: strings.Contains(flag, "/")}, nil
}

// match reports whether the share path p matches, ignoring case by
// lowercasing both sides; there is no Unicode normalization.
func (m pattern) match(p string) bool {
	if !m.anchored {
		p = path.Base(p)
	}
	ok, _ := path.Match(m.pattern, strings.ToLower(p))
	return ok
}

// matchWithin reports whether p, or a folder above it, matches.
func (m pattern) matchWithin(p string) bool {
	for {
		if m.match(p) {
			return true
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// mayMatchBelow reports whether the folder dir, or something below it,
// could match: that is, whether dir matches as many leading names of an
// anchored pattern as it has, or as the pattern has, since * and ? never
// cross a /. A name-only pattern could match at any depth.
func (m pattern) mayMatchBelow(dir string) bool {
	if !m.anchored {
		return true
	}
	pat := strings.Split(m.pattern, "/")
	names := strings.Split(strings.ToLower(dir), "/")
	n := min(len(pat), len(names))
	ok, _ := path.Match(strings.Join(pat[:n], "/"), strings.Join(names[:n], "/"))
	return ok
}
