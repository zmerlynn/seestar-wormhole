package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// activitySize is how many records Recent activity keeps.
const activitySize = 200

// entry is one line of Recent activity.
type entry struct {
	Time   string    `json:"time"`   // local, HH:MM:SS
	At     time.Time `json:"at"`     // the record's time, which orders the app's merged Activity
	Source string    `json:"source"` // "copy", or "delete" for Free up space
	Warn   bool      `json:"warn"`   // a warning or an error
	Text   string    `json:"text"`   // the message, then key=value for each attribute
}

// activity keeps the last activitySize records logged through its handler.
type activity struct {
	mu      sync.Mutex
	entries []entry // oldest first
	seq     int     // records kept so far, so a reader can tell new ones
}

// lines returns the entries, newest first, and seq.
func (a *activity) lines() ([]entry, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]entry, len(a.entries))
	for i, e := range a.entries {
		out[len(out)-1-i] = e
	}
	return out, a.seq
}

func (a *activity) add(e entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.entries) == activitySize {
		a.entries = append(a.entries[:0], a.entries[1:]...)
	}
	a.entries = append(a.entries, e)
	a.seq++
}

// tee is a slog.Handler that passes each record to next, if next takes
// it, and keeps each Info or higher one in its activity, tagged with its
// source.
type tee struct {
	next   slog.Handler
	keep   *activity
	source string
	attrs  []slog.Attr // from WithAttrs, with their groups' prefixes
	group  string      // the open groups, as a key prefix
}

func newTee(next slog.Handler, keep *activity, source string) *tee {
	return &tee{next: next, keep: keep, source: source}
}

func (t *tee) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || t.next.Enabled(ctx, l)
}

func (t *tee) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelInfo {
		var b strings.Builder
		b.WriteString(r.Message)
		for _, a := range t.attrs {
			writeAttr(&b, "", a)
		}
		r.Attrs(func(a slog.Attr) bool {
			writeAttr(&b, t.group, a)
			return true
		})
		t.keep.add(entry{Time: r.Time.Local().Format(time.TimeOnly), At: r.Time, Source: t.source, Warn: r.Level >= slog.LevelWarn, Text: b.String()})
	}
	if t.next.Enabled(ctx, r.Level) {
		return t.next.Handle(ctx, r)
	}
	return nil
}

func (t *tee) WithAttrs(as []slog.Attr) slog.Handler {
	c := *t
	c.next = t.next.WithAttrs(as)
	c.attrs = append([]slog.Attr{}, t.attrs...)
	for _, a := range as {
		a.Key = t.group + a.Key
		c.attrs = append(c.attrs, a)
	}
	return &c
}

func (t *tee) WithGroup(name string) slog.Handler {
	if name == "" {
		return t
	}
	c := *t
	c.next = t.next.WithGroup(name)
	c.group = t.group + name + "."
	return &c
}

// writeAttr writes " key=value", quoting a value that is empty or has a
// space or a quote, as slog's text handler does.
func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, g := range a.Value.Group() {
			writeAttr(b, prefix+a.Key+".", g)
		}
		return
	}
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \"=\t\n") {
		v = fmt.Sprintf("%q", v)
	}
	b.WriteString(" " + prefix + a.Key + "=" + v)
}

// private are the keys of the attributes no copy log or Activity line
// keeps: a telescope's folder, a file's destination and the folders a
// copy history move names. The page shows them.
var private = map[string]bool{"dest": true, "from": true, "to": true}

// withoutFolder is a slog.Handler that passes each record to next without
// its private attributes, at any group.
type withoutFolder struct{ next slog.Handler }

func (h withoutFolder) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h withoutFolder) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a, ok := public(a); ok {
			out.AddAttrs(a)
		}
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h withoutFolder) WithAttrs(as []slog.Attr) slog.Handler {
	kept := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		if a, ok := public(a); ok {
			kept = append(kept, a)
		}
	}
	return withoutFolder{h.next.WithAttrs(kept)}
}

func (h withoutFolder) WithGroup(name string) slog.Handler {
	return withoutFolder{h.next.WithGroup(name)}
}

// public is a without its private attributes, and false when a is one.
// An error, under any key, becomes its [reason], and a text under an
// error's key is kept only when it is [safeText]: an error's own text may
// name a folder, a file, an address, a host or a serial.
func public(a slog.Attr) (slog.Attr, bool) {
	if private[a.Key] {
		return a, false
	}
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindGroup:
		var kept []slog.Attr
		for _, g := range a.Value.Group() {
			if g, ok := public(g); ok {
				kept = append(kept, g)
			}
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(kept...)}, true
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, reason(err)), true
		}
	case slog.KindString:
		if errorKey[a.Key] && !safeText(a.Value.String()) {
			return slog.String(a.Key, "operation failed"), true
		}
	}
	return a, true
}

// errorKey are the keys a log gives an error's text.
var errorKey = map[string]bool{"err": true, "error": true}
