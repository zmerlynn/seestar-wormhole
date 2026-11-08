package engine

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/git-pkgs/gitignore"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
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
	// outside are the folders a walk limited to some folders did not
	// read, in listing order.
	outside []string
	// reserved are the entries a sync walk left out, before the
	// selection, for their reserved top-level name ([plan.Reserved]), in
	// listing order. A folder is not read.
	reserved []string
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
	return s.walk(ctx, sess, nil, nil, nil, false)
}

// walk is [Selector.Walk] that also calls listed, if not nil, with each
// folder kept and the number kept so far, before it is read, and passes
// kept to [remote.Session.Walk]. If within is not nil, a folder the
// selection keeps is read only if within reports true for it; any other is
// left out unread and noted in outside. With reserve, as in a sync pass,
// an entry under a reserved top-level name is left out before the
// selection is consulted, so no include can keep it, and noted in
// reserved.
func (s Selector) walk(ctx context.Context, sess remote.Session, listed func(folder string, folders int), kept func(remote.Entry), within func(dir string) bool, reserve bool) ([]remote.Entry, LeftOut, error) {
	out := LeftOut{keptParents: map[string]bool{}}
	folders := 0
	entries, err := sess.Walk(ctx, func(p string, dir bool) bool {
		if reserve && plan.Reserved(p) {
			out.reserved = append(out.reserved, p)
			out.keptParents[path.Dir(p)] = true
			return true
		}
		if e, ok := s.excluder(p, dir); ok {
			out.Skipped = append(out.Skipped, Skipped{p, e.flag})
			out.keptParents[path.Dir(p)] = true
			return true
		}
		if !s.included(p, dir) {
			out.Unmatched++
			out.keptParents[path.Dir(p)] = true
			return true
		}
		if dir && within != nil && !within(p) {
			out.outside = append(out.outside, p)
			out.keptParents[path.Dir(p)] = true
			return true
		}
		if dir && listed != nil {
			folders++
			listed(p, folders)
		}
		return false
	}, kept)
	if err != nil {
		return nil, out, err
	}
	return entries, out, nil
}

// LeavesOut reports whether a walk leaves out the share path p, a file if
// not dir: whether an exclude matches p or a folder above it, or the
// includes keep one of them out, as [Selector.Walk] decides.
func (s Selector) LeavesOut(p string, dir bool) bool {
	for i := range len(p) {
		if p[i] == '/' && s.skips(p[:i], true) {
			return true
		}
	}
	return s.skips(p, dir)
}

// skips reports whether a walk that reached p would leave it out.
func (s Selector) skips(p string, dir bool) bool {
	_, excluded := s.excluder(p, dir)
	return excluded || !s.included(p, dir)
}

// excluder returns the first exclude that matches p, a file if not dir.
func (s Selector) excluder(p string, dir bool) (Pattern, bool) {
	for _, e := range s.Excludes {
		if e.match(p, dir) {
			return e, true
		}
	}
	return Pattern{}, false
}

// included reports whether the includes keep p: with none, everything; a
// file, if one matches it or a folder above it; a folder, if one could
// match it or something below it.
func (s Selector) included(p string, dir bool) bool {
	if len(s.Includes) == 0 {
		return true
	}
	for _, m := range s.Includes {
		if dir && m.mayMatchBelow(p) || !dir && m.match(p, false) {
			return true
		}
	}
	return false
}

// Pattern is one checked selection pattern, from [ParsePattern].
type Pattern struct {
	// flag is the pattern as given, for logs.
	flag string
	// m matches the lowercased pattern in .gitignore syntax.
	m *gitignore.Matcher
	// prefixes[k-1] matches a folder whose first k names match the
	// pattern's first k parts, for each k up to the pattern's first **
	// part, or its last part. It is empty for a pattern that can match at
	// any depth.
	prefixes []*gitignore.Matcher
}

// String returns the pattern as given.
func (m Pattern) String() string { return m.flag }

// ErrNegated is the error for a pattern that starts with !, which in a
// .gitignore file brings back what an earlier pattern left out. Here
// excludes always win, so a caller says which list to use instead.
var ErrNegated = errors.New("a leading ! is not supported")

// ParsePattern checks a selection pattern, in .gitignore syntax, and
// compiles it. A pattern with no slash, or only a trailing one, matches a
// name at any depth; one with a slash at the start or in the middle is a
// path from the share root. Case is ignored by lowercasing the pattern
// here and each path in match. Unlike a .gitignore line, a pattern can't
// start with ! or #, end in an unescaped space or \, hold a line break, or
// have an empty, . or .. part, since none of those would select anything
// as written. Nor can it escape a /: the library always takes a / to
// separate names, so \/ would match nothing, where git reads it as a
// plain /.
// The error says which rule the pattern broke and never repeats the
// pattern.
func ParsePattern(s string) (Pattern, error) {
	bad := func(rule string) (Pattern, error) { return Pattern{}, errors.New(rule) }
	switch {
	case strings.ContainsAny(s, "\n\r"):
		return bad("a pattern is one line, with no line breaks")
	case strings.HasPrefix(s, "!"):
		return Pattern{}, ErrNegated
	case strings.HasPrefix(s, "#"):
		return bad(`a leading # makes a comment in .gitignore syntax; write \# to match a name that starts with #`)
	case strings.HasSuffix(s, " ") && !escaped(s[:len(s)-1]):
		return bad(`trailing spaces are ignored in .gitignore syntax; remove them, or write \ (a backslash, then a space) to match one`)
	case escaped(s):
		return bad(`a trailing \ has nothing to escape; write \\ to match a \`)
	case escapesSlash(s):
		return bad(`a / can't be escaped; write a plain / between folders`)
	}
	body := s
	if len(body) > 1 {
		body = strings.TrimSuffix(body, "/")
	}
	body = strings.TrimPrefix(body, "/")
	parts := strings.Split(body, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return bad("use a name, or a path from the share root, with no empty, . or .. parts")
		}
	}
	lower := strings.ToLower(s)
	m, err := compile(lower)
	if err != nil {
		return Pattern{}, err
	}
	p := Pattern{flag: s, m: m}
	if len(parts) == 1 && !strings.HasPrefix(s, "/") {
		return p, nil
	}
	// Anchored: a folder can hold a match only if its leading names match
	// the pattern's leading parts, up to a ** part, which may stand for any
	// number of names. Each prefix is itself an anchored folder pattern,
	// compiled by the same library. It ends in /, so that a space before a
	// / in the pattern is not taken for a trailing space and dropped.
	lowerParts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(lower, "/"), "/"), "/")
	for k := range lowerParts {
		if doubleStar(lowerParts[k]) {
			break
		}
		prefix, err := compile("/" + strings.Join(lowerParts[:k+1], "/") + "/")
		if err != nil {
			return Pattern{}, err
		}
		p.prefixes = append(p.prefixes, prefix)
	}
	return p, nil
}

// compile compiles one lowercased .gitignore pattern.
func compile(pattern string) (*gitignore.Matcher, error) {
	m := gitignore.New("")
	m.AddPatterns([]byte(pattern), "")
	if len(m.Errors()) > 0 {
		// An unclosed [ or an unknown [:class:]; the library's message
		// repeats part of the pattern, so it is not passed on.
		return nil, errors.New(`close each [ class with ], and name only POSIX classes such as [:digit:]; write \[ to match a [`)
	}
	return m, nil
}

// escaped reports whether s ends in an unescaped backslash: an odd number
// of them.
func escaped(s string) bool {
	n := len(s) - len(strings.TrimRight(s, `\`))
	return n%2 == 1
}

// escapesSlash reports whether s has a / with an unescaped backslash
// before it. In a\\/b the backslash before the / is itself escaped, so
// the / separates names.
func escapesSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			if i < len(s) && s[i] == '/' {
				return true
			}
		}
	}
	return false
}

// doubleStar reports whether a pattern part is ** (or more stars), which
// matches any number of names.
func doubleStar(part string) bool {
	return len(part) >= 2 && strings.Trim(part, "*") == ""
}

// match reports whether the share path p, a file if not dir, or a folder
// above it matches, ignoring case by lowercasing both sides; there is no
// Unicode normalization.
func (m Pattern) match(p string, dir bool) bool {
	return m.m.MatchPath(strings.ToLower(p), dir)
}

// mayMatchBelow reports whether the folder dir, or something below it,
// could match: whether dir's leading names match the pattern's leading
// parts, as many as dir has or as come before a ** part. A pattern that can
// match at any depth could match below any folder.
func (m Pattern) mayMatchBelow(dir string) bool {
	if len(m.prefixes) == 0 {
		return true
	}
	names := strings.Split(strings.ToLower(dir), "/")
	k := min(len(names), len(m.prefixes))
	return m.prefixes[k-1].MatchPath(strings.Join(names[:k], "/"), true)
}
