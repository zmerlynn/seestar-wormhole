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
	Time string `json:"time"` // local, HH:MM:SS
	Warn bool   `json:"warn"` // a warning or an error
	Text string `json:"text"` // the message, then key=value for each attribute
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
// it, and keeps each Info or higher one in its activity.
type tee struct {
	next  slog.Handler
	keep  *activity
	attrs []slog.Attr // from WithAttrs, with their groups' prefixes
	group string      // the open groups, as a key prefix
}

func newTee(next slog.Handler, keep *activity) *tee { return &tee{next: next, keep: keep} }

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
		t.keep.add(entry{Time: r.Time.Local().Format(time.TimeOnly), Warn: r.Level >= slog.LevelWarn, Text: b.String()})
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
