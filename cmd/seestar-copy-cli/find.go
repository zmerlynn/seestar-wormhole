package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

type findOptions struct {
	endpoint  string
	selection selectionFlags
	// selector is set by validate from selection.
	selector    selector
	olderThan   time.Duration
	format      string
	dest        string
	destSet     bool
	stateDir    string
	stateDirSet bool
}

func newFindCommand(log *slog.Logger) *cobra.Command {
	var o findOptions
	cmd := &cobra.Command{
		Use:   "find --endpoint HOST:PORT [flags]",
		Short: "List the files and folders on the telescope's share",
		Long: `List the files and folders on the telescope's share.
find lists the share once, leaving out what sync would: --exclude,
--no-default-excludes and --include select exactly as for sync (see
` + programName + ` help sync). With --older-than, it lists only files whose
device mtime is at least that old. Folders are always listed.
Each entry is one line on stdout, in listing order. --format text, the
default, writes the type (d for a folder, - for a file), the size in bytes,
the device mtime in UTC as RFC 3339, and the share path, quoted in Go
string syntax if it holds a space, a quote, a backslash, a character that
is not printable, or bytes that are not UTF-8. --format jsonl writes one JSON object per
line with the members path, type (file or dir), size, mtime and status.
With --dest, find reads that destination's state file, as a sync dry run
does, and adds a status to each file, as a column before the path: copied
(recorded with the device's size and mtime), changed (recorded with another
size or mtime) or new (not recorded).
find never writes: not to the telescope, not to --dest, not to the state.
Durations use Go units, such as 36h or 90m, or whole days, such as 7d.
Logs and errors go to stderr.
Exit 0 when the listing is complete, 1 when it fails or is stopped, 2 on a
usage error.`,
		Example: "  " + programName + " find --endpoint 127.0.0.1:4445 --include /MyWorks\n" +
			"  " + programName + " find --endpoint 127.0.0.1:4445 --dest ~/Seestar --format jsonl",
		Args: noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.destSet = cmd.Flags().Changed("dest")
			o.stateDirSet = cmd.Flags().Changed("state-dir")
			if err := o.validate(); err != nil {
				return err
			}
			return runFind(cmd.Context(), o, cmd.OutOrStdout(), log)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.endpoint, "endpoint", "", "SMB host:port of the telescope or a proxy (required)")
	o.selection.register(f)
	f.Var((*ageValue)(&o.olderThan), "older-than", "list only files whose device time is at least this old (default: no limit)")
	f.StringVar(&o.format, "format", "text", "output format: text or jsonl")
	f.StringVar(&o.dest, "dest", "", "local mirror folder whose state gives each file a status; never created")
	f.StringVar(&o.stateDir, "state-dir", "", "folder for the state file of --dest (default: the per-user config folder's seestar-copy)")
	return cmd
}

// validate checks the flags without touching the network or the disk.
func (o *findOptions) validate() error {
	if o.endpoint == "" {
		return &usageError{Option: "--endpoint", Rule: "required; give the SMB host:port"}
	}
	if err := validateEndpoint(o.endpoint); err != nil {
		return err
	}
	sel, err := o.selection.parse()
	if err != nil {
		return err
	}
	o.selector = sel
	if o.olderThan < 0 {
		return &usageError{Option: "--older-than", Rule: "must not be negative"}
	}
	if o.format != "text" && o.format != "jsonl" {
		return &usageError{Option: "--format", Rule: "use text or jsonl"}
	}
	if o.destSet && o.dest == "" {
		return &usageError{Option: "--dest", Rule: "must not be empty; omit it to list without a status"}
	}
	if o.stateDirSet && o.stateDir == "" {
		return &usageError{Option: "--state-dir", Rule: "must not be empty; omit it for the default"}
	}
	if o.stateDirSet && !o.destSet {
		return &usageError{Option: "--state-dir", Rule: "give --dest with it; find reads a state file only for a destination"}
	}
	return nil
}

// runFind lists the share to out. It never writes to the disk: the
// read-only guard is on from the start, and with --dest the state is read
// as a dry run reads it, without the lock.
func runFind(ctx context.Context, o findOptions, out io.Writer, log *slog.Logger) error {
	readonly.Enable()
	var records map[string]state.Record
	if o.destSet {
		stateDir, err := resolveStateDir(o.stateDir, o.stateDirSet)
		if err != nil {
			return err
		}
		if records, err = state.Load(stateDir, o.dest, log); err != nil {
			return err
		}
	}
	return list(ctx, o, records, out, log)
}

// File statuses against a destination's state.
const (
	statusCopied  = "copied"
	statusChanged = "changed"
	statusNew     = "new"
)

// listed is one line of find's output. Its JSON form is the jsonl format.
type listed struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	// MTime is RFC 3339 in UTC, with as many fractional digits as needed.
	MTime  string `json:"mtime"`
	Status string `json:"status,omitempty"`
}

// list lists the share and writes a line for each entry kept, logs a line
// for each entry an exclude skipped, and a summary. records, if not nil,
// give each file a status.
func list(ctx context.Context, o findOptions, records map[string]state.Record, out io.Writer, log *slog.Logger) (err error) {
	start := time.Now()
	var left leftOut
	var files, folders, recent int
	var bytes int64
	statuses := map[string]int{}
	defer func() {
		args := []any{"files", files, "folders", folders, "bytes", bytes, "recent", recent,
			"skipped", len(left.skipped), "unmatched", left.unmatched}
		if records != nil {
			args = append(args, statusCopied, statuses[statusCopied], statusChanged, statuses[statusChanged], statusNew, statuses[statusNew])
		}
		args = append(args, "elapsed", time.Since(start).Round(time.Millisecond))
		if err != nil {
			log.Error("listing failed", append(args, "error", err)...)
			return
		}
		log.Info("listing done", args...)
	}()
	sess, err := dial(ctx, o.endpoint)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", o.endpoint, err)
	}
	defer sess.Close()
	entries, left, err := o.selector.walk(ctx, sess)
	for _, x := range left.skipped {
		log.Info("skipped", "path", x.path, "exclude", x.by.flag)
	}
	if err != nil {
		return fmt.Errorf("list the share: %w", err)
	}
	now := time.Now()
	w := bufio.NewWriter(out)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("write the listing: %w", err)
		}
		l := listed{Path: e.Path, Size: e.Size, MTime: e.ModTime.UTC().Format(time.RFC3339Nano)}
		switch e.Kind {
		case remote.Dir:
			l.Type = "dir"
			folders++
		case remote.Regular:
			if !plan.OldEnough(now, e.ModTime, o.olderThan) {
				recent++
				continue
			}
			l.Type = "file"
			files++
			bytes += e.Size
			if records != nil {
				l.Status = fileStatus(records, e)
				statuses[l.Status]++
			}
		default:
			// Links and the like: sync ignores them too.
			continue
		}
		if o.format == "jsonl" {
			err = enc.Encode(l)
		} else {
			err = writeText(w, l, records != nil)
		}
		if err != nil {
			return fmt.Errorf("write the listing: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write the listing: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write the listing: %w", err)
	}
	return nil
}

// fileStatus compares a listed file with its record, as a sync pass does.
func fileStatus(records map[string]state.Record, e remote.Entry) string {
	rec, ok := records[e.Path]
	switch {
	case !ok:
		return statusNew
	case rec.Size == e.Size && rec.MTime.Equal(e.ModTime):
		return statusCopied
	}
	return statusChanged
}

// writeText writes l as a text line: type, size, mtime, the status if
// withStatus (- for a folder), and the quoted path.
func writeText(w io.Writer, l listed, withStatus bool) error {
	typ := "-"
	if l.Type == "dir" {
		typ = "d"
	}
	status := ""
	if withStatus {
		status = " " + l.Status
		if l.Status == "" {
			status = " -"
		}
	}
	_, err := fmt.Fprintf(w, "%s %d %s%s %s\n", typ, l.Size, l.MTime, status, quotePath(l.Path))
	return err
}

// quotePath returns p as it is, or, if it holds a space, a quote, a
// backslash, a character that is not printable or bytes that are not
// UTF-8, as a double-quoted Go string. So a line always splits on its
// first spaces, and the path is the rest.
func quotePath(p string) string {
	needs := strings.ContainsFunc(p, func(r rune) bool {
		return r == ' ' || r == '"' || r == '\\' || !strconv.IsPrint(r)
	})
	if needs || !utf8.ValidString(p) {
		return strconv.Quote(p)
	}
	return p
}
