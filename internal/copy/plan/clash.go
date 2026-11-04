package plan

import (
	"fmt"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// clashes returns the Fail reason of each listed file whose local name
// could be the same file or folder as that of another listed file, a
// recorded path or one of folders, the listing's non-regular entries.
//
// Windows, default macOS and some Linux mounts treat names that differ only
// in case or Unicode normalization as one name, so two such paths would
// overwrite each other or adopt the wrong file. Names are compared one
// component at a time, so a folder spelled two ways ("M 31/a.fit" and
// "m 31/b.fit") clashes, and so does a file and a folder ("A" and "a/b.fit").
//
// Paths are compared by a key: NFC, then Unicode case folding, then NFC
// again. The key is only for the comparison; names are kept byte for byte.
// A reason names the clashing prefix and one other spelling of it, the
// smallest in byte order, and counts any more, so it stays short however
// many spellings there are.
//
// Every lexically safe path takes part (see lexicallySafe), whatever its
// size, mtime or the Windows rules: a name Windows cannot store still fails
// for that, but it is also a spelling that its valid alias must not take.
// A path that is both listed and recorded with one spelling is not a clash.
// A recorded path or a folder only makes listed files fail.
func clashes(listed, folders []string, state map[string]Recorded) map[string]string {
	ns := namespace{fold: cases.Fold(), groups: map[string]*group{}, byKey: map[string]*group{}}
	var members []string
	for _, p := range listed {
		if lexicallySafe(p) {
			ns.add(p)
			members = append(members, p)
		}
	}
	for _, p := range folders {
		if lexicallySafe(p) {
			ns.add(p)
		}
	}
	for p := range state {
		if lexicallySafe(p) {
			ns.add(p)
		}
	}
	reasons := map[string]string{}
	shared := map[string]string{} // the reason of each clashing prefix
	for _, p := range members {
		if r := ns.reason(p, shared); r != "" {
			reasons[p] = r
		}
	}
	return reasons
}

// lexicallySafe reports whether p passes the path checks that hold on every
// platform: valid UTF-8, relative, clean, no NUL and not a temporary file's
// name. Only such a path could become a local name, so only such paths are
// compared.
func lexicallySafe(p string) bool { return checkPath(p, false) == "" }

// withClash adds the name clash, if any, to a path's own Fail reason, if any.
func withClash(reason, clash string) string {
	switch {
	case clash == "":
		return reason
	case reason == "":
		return clash
	}
	return reason + "; " + clash
}

// group is the spellings of one path prefix that have the same key.
type group struct {
	n           int    // how many spellings
	least, next string // the two smallest spellings, in byte order
}

// insert adds spelling s, which is not in the group yet.
func (g *group) insert(s string) {
	g.n++
	switch {
	case g.n == 1:
		g.least = s
	case s < g.least:
		g.least, g.next = s, g.least
	case g.n == 2 || s < g.next:
		g.next = s
	}
}

// namespace is the path prefixes of one pass, grouped by key.
type namespace struct {
	fold   cases.Caser
	groups map[string]*group // by exact prefix; spellings with one key share a group
	byKey  map[string]*group
}

// key returns the comparison key of one path element.
func (ns *namespace) key(elem string) string {
	return norm.NFC.String(ns.fold.String(norm.NFC.String(elem)))
}

// add puts each prefix of p, such as "MyWorks", "MyWorks/M 31" and
// "MyWorks/M 31/a.fit", in its group. A prefix already seen is skipped, so
// the elements of a shared folder are folded once.
//
// A prefix's key is its parent's exact spelling and the key of its last
// element. The parent's own key is not needed: a parent spelled two ways is
// a clash itself, and reason names the shallowest clash.
func (ns *namespace) add(p string) {
	for start, end := 0, 0; end <= len(p); end++ {
		if end < len(p) && p[end] != '/' {
			continue
		}
		exact := p[:end]
		if _, ok := ns.groups[exact]; !ok {
			k := p[:start] + ns.key(p[start:end])
			g := ns.byKey[k]
			if g == nil {
				g = &group{}
				ns.byKey[k] = g
			}
			g.insert(exact)
			ns.groups[exact] = g
		}
		start = end + 1
	}
}

// reason returns the clash of p's shallowest prefix that has another
// spelling, or "" if none has. The reason of a prefix is made once and
// kept in shared, so every file under a clashing folder shares it.
func (ns *namespace) reason(p string, shared map[string]string) string {
	for end := 0; end <= len(p); end++ {
		if end < len(p) && p[end] != '/' {
			continue
		}
		exact := p[:end]
		g := ns.groups[exact]
		if g == nil || g.n < 2 {
			continue
		}
		if r, ok := shared[exact]; ok {
			return r
		}
		other := g.least
		if other == exact {
			other = g.next
		}
		more := ""
		switch g.n {
		case 2:
		case 3:
			more = " and 1 other"
		default:
			more = fmt.Sprintf(" and %d others", g.n-2)
		}
		r := fmt.Sprintf("name clash: %+q and %+q%s differ only in case or Unicode normalization", exact, other, more)
		shared[exact] = r
		return r
	}
	return ""
}
