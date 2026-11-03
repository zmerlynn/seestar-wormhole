package engine

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// DefaultExcludes are the built-in exclude patterns. Each one is checked
// and matched exactly as the same pattern given by the user is.
var DefaultExcludes = []string{
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

// Selector is a checked selection: what a walk of the share leaves out.
// An entry that an exclude matches is skipped, whatever the includes say.
// With includes, a file is kept only if one of them matches it or a folder
// above it, and a folder is listed only if one of them could match it or
// something below it. The zero value keeps everything.
type Selector struct {
	Excludes []Pattern
	Includes []Pattern
}

// LeftOut is what a walk left out.
type LeftOut struct {
	// Skipped are the entries an exclude matched, in listing order.
	Skipped []Skipped
	// Unmatched counts the entries that no include could select and that
	// no exclude matched. A folder counts once; what is in it is not listed.
	Unmatched int
	// keptParents holds the parent of every entry left out, for either
	// reason: a folder with something in it that the walk did not list.
	// The root is ".".
	keptParents map[string]bool
}

// Skipped is a listed entry that an exclude matched.
type Skipped struct {
	Path string
	// Exclude is the first exclude that matched, as given.
	Exclude string
}

// Walk lists the share over sess, leaving out what the selection does, and
// returns what it kept and what it left out. A folder left out is never
// read. Any listing failure, of the root or of a folder that is kept, fails
// the walk; what was left out before it is still returned.
func (s Selector) Walk(ctx context.Context, sess remote.Session) ([]remote.Entry, LeftOut, error) {
	return s.walk(ctx, sess, nil)
}

// walk is [Selector.Walk] that also calls listed, if not nil, with each
// folder kept and the number kept so far, before it is read.
func (s Selector) walk(ctx context.Context, sess remote.Session, listed func(folder string, folders int)) ([]remote.Entry, LeftOut, error) {
	out := LeftOut{keptParents: map[string]bool{}}
	folders := 0
	entries, err := sess.Walk(ctx, func(p string, dir bool) bool {
		for _, e := range s.Excludes {
			if e.match(p) {
				out.Skipped = append(out.Skipped, Skipped{p, e.flag})
				out.keptParents[path.Dir(p)] = true
				return true
			}
		}
		if !s.included(p, dir) {
			out.Unmatched++
			out.keptParents[path.Dir(p)] = true
			return true
		}
		if dir && listed != nil {
			folders++
			listed(p, folders)
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
func (s Selector) included(p string, dir bool) bool {
	if len(s.Includes) == 0 {
		return true
	}
	for _, m := range s.Includes {
		if dir && m.mayMatchBelow(p) || !dir && m.matchWithin(p) {
			return true
		}
	}
	return false
}

// Pattern is one checked selection pattern, from [ParsePattern].
type Pattern struct {
	// flag is the pattern as given, for logs.
	flag string
	// pattern is lowercased and has no leading slash. If anchored, it is
	// matched against the whole share path; otherwise against the last name.
	pattern  string
	anchored bool
}

// ParsePattern checks a selection pattern. A pattern with no slash matches
// a name at any depth; one with a slash anywhere is a path from the share
// root, with one leading slash allowed. Only literal characters, * and ?
// are allowed, so path.Match never sees a class, an escape or a bad
// pattern. Nothing is cleaned: a pattern that would need it is refused. The
// error says which rule the pattern broke.
func ParsePattern(s string) (Pattern, error) {
	bad := func(rule string) (Pattern, error) { return Pattern{}, errors.New(rule) }
	switch {
	case strings.ContainsAny(s, `[\`):
		return bad(`[ and \ are not supported; use * and ?, and / between folders`)
	case strings.Contains(s, "**"):
		return bad("** is not supported; * matches within one name")
	case len(s) > 1 && strings.HasSuffix(s, "/"):
		return bad("omit the trailing /; a pattern matches files and folders")
	}
	p := strings.TrimPrefix(s, "/")
	for elem := range strings.SplitSeq(p, "/") {
		if elem == "" || elem == "." || elem == ".." {
			return bad("use a name, or a path from the share root, with no empty, . or .. parts")
		}
	}
	return Pattern{flag: s, pattern: strings.ToLower(p), anchored: strings.Contains(s, "/")}, nil
}

// match reports whether the share path p matches, ignoring case by
// lowercasing both sides; there is no Unicode normalization.
func (m Pattern) match(p string) bool {
	if !m.anchored {
		p = path.Base(p)
	}
	ok, _ := path.Match(m.pattern, strings.ToLower(p))
	return ok
}

// matchWithin reports whether p, or a folder above it, matches.
func (m Pattern) matchWithin(p string) bool {
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
func (m Pattern) mayMatchBelow(dir string) bool {
	if !m.anchored {
		return true
	}
	pat := strings.Split(m.pattern, "/")
	names := strings.Split(strings.ToLower(dir), "/")
	n := min(len(pat), len(names))
	ok, _ := path.Match(strings.Join(pat[:n], "/"), strings.Join(names[:n], "/"))
	return ok
}
