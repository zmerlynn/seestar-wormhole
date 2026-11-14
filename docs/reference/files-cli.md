# File command reference

Use this page when you script `seestar-wormhole-cli`'s file commands, `sync`,
`find`, `browse`, `delete` and `scan`, or need an exact flag, rule or log line. [Copy images](../copying.md#from-the-command-line) has a first run.

- [sync](#sync): flags, endpoint, [performance](#performance), destination
  layout, [selection](#selection) and [dry run](#dry-run)
- [How a pass decides](#how-a-pass-decides)
- [Copying a file](#copying-a-file) and [temporary files](#temporary-files)
- [The state file](#the-state-file)
- [Stopping](#stopping)
- [Logs](#logs) and [exit codes](#exit-codes)
- [Names that fail](#names-that-fail)
- [Windows](#windows)
- [find](#find): listing the share, its [text](#text-format) and
  [JSONL](#jsonl-format) formats, and [statuses](#statuses)
- [browse](#browse): a read-only web page over the share, and
  [who can use it](#who-can-use-the-page)
- [delete](#delete): [proof modes](#proof-modes),
  [how delete decides](#how-delete-decides), [emptied folders](#emptied-folders)
  and [the check-then-remove race](#the-check-then-remove-race)
- [scan](#scan): the telescopes on this network
- [Help, licenses and releases](#help-licenses-and-releases), which the
  [connection command reference](connection-cli.md) covers for every command

## sync

```sh
seestar-wormhole-cli sync --endpoint HOST:PORT --dest DIR [flags]
```

Mirrors the telescope's images into the folder `--dest`, in passes. Each pass
connects to the share, lists the whole share except what is
[excluded](#selection), copies what is new or changed,
records each copy in a [state file](#the-state-file), logs off and
disconnects. Passes repeat every `--interval` until you
[stop](#stopping) it, or run once with `--once`. A
[dry run](#dry-run) reports what one pass would do and changes nothing.

Sync only reads from the telescope. It opens files on the share read-only and
never creates, changes, renames, deletes or locks anything there. It never
deletes anything in the destination either, except its own
[temporary files](#temporary-files).

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | none; required | SMB `host:port` of the telescope or of a seestar-wormhole listener. See [endpoint](#endpoint). |
| `--dest` | none; required | The local mirror folder. It is created, with any missing parents, if it does not exist, except by a dry run. A relative path is taken from the current folder. |
| `--exclude` | none | A pattern for files and folders to leave out, in addition to the built-in ones. Repeat it for more than one. See [selection](#selection). |
| `--no-default-excludes` | off | Do not leave out what the built-in patterns match. See [selection](#selection). |
| `--include` | none | Copy only files that this pattern matches, or that are in a folder it matches. Repeat it for more than one; a file needs to match only one. Excludes always win. See [selection](#selection). |
| `--interval` | `5m` | The wait between the end of one pass and the start of the next. Must be more than zero. |
| `--once` | off | Run one pass and exit. See [exit codes](#exit-codes). |
| `--dry-run` | off | Run one pass that only reports what it would do, and change nothing on disk. Not allowed with `--interval`. See [dry run](#dry-run). |
| `--ready-age` | `2m` | A file is copied only once its device mtime is at least this old. Zero or more. |
| `--first-pass-age` | `10m` | On the first pass, a file counts as finished only if its device mtime is at least this old. Zero or more. See [readiness](#readiness). |
| `--workers` | `4` | Files copied at once, `1` to `64`. All of them share one SMB session. |
| `--history-dir` | `copy-history` in the per-user config folder's `seestar-wormhole` | The copy history folder, which holds the state file. See [location](selection-and-history.md#location). It must not be empty when given. Earlier releases called it `--state-dir`, which is now refused. |
| `--profile` | none: no saved settings are read | `default` fills the flags not given from the app's saved settings. Only `default` exists. See [use the app's saved settings](#use-the-apps-saved-settings). |
| `--scan` | the app's saved choice, `start` without one | When to scan this network for the telescope `--profile default` saved there: `start`, `connect` or `off`. See [use the app's saved settings](#use-the-apps-saved-settings). Any other value exits 2. It is checked even when nothing is scanned. |
| `--log-format` | `text` | `text` or `json` log lines on stderr. See [logs](#logs). |
| `--log-level` | `info` | The lowest level logged: `debug`, `info`, `warn` or `error`. |

Durations use Go units, such as `30s`, `5m` or `1h30m`; there is no unit for
days. All flags are checked before anything touches the network or the disk,
and a bad one exits 2.

### Endpoint

The endpoint is either:

- the telescope on your local network, at its IP address and port 445, such
  as `--endpoint 192.0.2.10:445` (use your telescope's address); or
- the SMB listener of seestar-wormhole's [proxy](connection-cli.md#proxy), which
  is `127.0.0.1:4445` by default.

Both are handled the same way. Sync logs in as guest with an empty password
and mounts the share `EMMC Images`; neither is configurable. An IPv6 address
goes in brackets, such as `[2001:db8::10]:445`. The port must be 1 to 65535.

Each pass opens its own session and closes it before the wait, so the
telescope's share is free between passes. A failed connection fails that pass
only; see [a failed pass](#a-failed-pass).

### Use the app's saved settings

`--profile default` fills each flag not given from what the app saved: its
Copy settings, [`copy.json`](app-settings.md#copyjson-version-1), and its connection,
[`config.json`](app-settings.md#configjson-version-1), in the app's state folder
without `--state-dir`. It is on `sync`, `find`, `browse` and `delete`, and
`default` is the only profile; any other name exits 2 before any file is
read. An app run with `ui --state-dir PATH` keeps its settings outside the
default profile. Without `--profile`, the file commands read neither file.

```text
seestar-wormhole-cli sync --profile default
seestar-wormhole-cli find --profile default --format jsonl
seestar-wormhole-cli delete --profile default --older-than 14d
seestar-wormhole-cli sync --profile default --exclude '*.mp4'
```

A flag given on the command line always wins, and one `--exclude` or
`--include` replaces the whole saved list. A filled flag is not a given one
for the rules that refuse a combination: a saved interval with `--dry-run`
is accepted, and a saved Copy once gives way to a given `--once`,
`--interval` or `--dry-run`. A filled `--dest` gives find and browse their
statuses, as a given one does.

| Saved | sync | find | browse | delete |
| --- | --- | --- | --- | --- |
| The saved telescope's folder, from `telescopes` or `folder` | `--dest` | `--dest` | `--dest` | `--dest`, not with `--ignore-state` |
| **Copy history folder**, `history_dir` | `--history-dir` | `--history-dir` | `--history-dir` | `--history-dir`, not with `--ignore-state` |
| The connection | `--endpoint` | `--endpoint` | `--endpoint` | `--endpoint` |
| **Leave out** and **Only copy**, `excludes` and `includes` | `--exclude`, `--include` | the same | the same, with a destination | never |
| The built-in patterns off, `skip_built_in: false` | `--no-default-excludes` | the same | the same, with a destination | never |
| `interval_minutes` | `--interval` | | | |
| `once: true` | `--once` | | | |
| **Files at once**, `files_at_once` | `--workers` | | | |

The folder is that of the telescope `config.json` saves, keyed as the app
keys it: by `lan_serial` on this network, or by the address when it was
saved without one, and by the saved `serial` through the account; see
[`telescopes`](app-settings.md#copyjson-version-1). Until any telescope has
a folder of its own, the destination is `folder`. After that, a usable
`config.json` must identify a telescope with a saved folder, or you must
give `--dest`. If the saved telescope has no folder while another has one, a command that
would fill `--dest` exits 2 for `--profile`; a given `--dest`, or delete
with `--ignore-state`, runs as usual. browse instead fills only
`--endpoint` and lists without statuses.

delete takes only where to act: Free up space's patterns, its age and
**Before deleting, check** are never applied, so give the selection, the
proof and `--older-than` on the command line. `start_copying` is ignored.

The endpoint comes from the saved connection alone. On this network it is
the saved address. Through the account it is the
[SMB listener](connection-cli.md#port-mappings) for telescope port 445 on the
saved bind address, or on `127.0.0.1` for a wildcard bind, `127.0.0.1:4445`
by default, and something must be serving it: the app connected with
**File sharing for other apps (SMB)** on, or `proxy --config`. The file
commands never start a connection. With no usable `config.json`, no saved
address, or that service off, `--endpoint` is not filled and is required as
usual; if nothing listens, the run fails with exit 1.

On this network, an address the app saved from a scan is saved with the
telescope's serial number, [`lan_serial`](app-settings.md#configjson-version-1).
When `--endpoint` was filled from it, a connect can
[scan this network](#scan) for that serial and connect to an address the
telescope answered from, the saved address first; an address that doesn't
connect doesn't end the search. When the saved port is 445 or 4445, the SMB
port of a seestar-wormhole proxy that answers scans for its telescope, each
address is tried on 445 and then on 4445, within the same connect; another
saved port is tried alone. Such a connect has 30 seconds in all, for the
scan and every address and port it tries, and fails as timed out after
that; a connection made within them isn't limited by them. An `--endpoint` given on the command line is
dialed as given. An address another telescope answers at, the saved one or
any other, isn't used, even if it answers there only while that address is
being connected; a connection already made there is closed and the search
goes on. If no reply carries the serial, the saved address is tried,
unless another telescope answered there: then the connect fails, and its
error names the telescope that answered and the saved serial.

`--scan` chooses when a connect scans. Without it, the app's choice under
**On this network**, [`lan_scan`](app-settings.md#configjson-version-1), is
used, and `start` when there is none:

| `--scan` | When a connect scans |
| --- | --- |
| `start` | The run's first connect scans. Later connects, such as each pass of a `sync` with `--interval` and a reconnect, go to the address that scan connected to, and scan again, once, only when that connect fails, with the same rules. A run is one command. |
| `connect` | Every connect scans. |
| `off` | No connect scans: the saved address is dialed as it is, and nothing is logged about a scan. A telescope that moved isn't followed. |

`delete` keeps the address it first connected at for the rest of the run,
whatever `--scan` says. Nothing is saved; the app saves a new address the
next time it connects. A given `--endpoint`, or an address saved without a
serial, is dialed as it is, without a scan, whatever `--scan` says; the
value is still checked, and one other than `start`, `connect` or `off`
exits 2. Errors name the address dialed, or the host name and DNS server
of a failed lookup, under every `--scan`. The scan's records, which name
the telescope, are in [logs](#logs).

At the start of the run, one `INFO` record, `saved settings`, names the
filled flags and none of their values, such as
`profile=default filled=--dest,--history-dir,--endpoint,--interval,--workers`.
Neither file is written or locked, and both are read with the app's own
checks. Each refusal exits 2 with `option=--profile`:

| Case | rule |
| --- | --- |
| A name other than `default` | `only default exists in this version` |
| No per-user config folder | `no per-user config folder for the default profile; give the flags` |
| No `copy.json` | `no saved Copy settings; save them on the app's Copy tab, or give the flags` |
| A `copy.json` the app refuses | `use a valid saved copy.json in the app's state folder` |
| The saved telescope has no folder yet, and `--dest` is not given, except for browse | `the telescope saved in the app has no folder yet; choose one on the app's Copy tab, or give --dest` |

### Performance

`--workers` sets how many files are copied at once. Keep the default of 4.
In one setup, copying through a seestar-wormhole listener:

- 1 worker copied 1.17 GB in 4 minutes 16 seconds, about 4.3 MiB/s;
- 4 workers copied 330,833,790 bytes in 36 to 42 seconds, about 7.5 to
  8.8 MiB/s;
- earlier tests of that path topped out near 10 MiB/s.

These are one setup's measurements, not guarantees. One worker was slower
than four in these measurements. More workers may not help once the link is
near its ceiling. All workers share one SMB session.

Through Telescope Network, that session is the proxy's, shared with the
Seestar app and control clients such as seestar_alp. If they are slow to
respond while a copy runs, lower `--workers` to 1 or 2. The app's **Files at
once** setting is the same choice.

### Destination layout

Files mirror the share's tree under `--dest`, starting at the share root,
such as:

```text
<dest>/MyWorks/<target>/<file>
<dest>/Autorun/Light/<target>/<file>
<dest>/<file at the share root>
```

Names are kept byte for byte. A downloaded file gets the device file's
modification time (mtime). An [adopted](#how-a-pass-decides) file keeps its
own, which is within 2 seconds of the device's. Folders are created as needed;
their mtimes are not set.

New files are created with mode 0666 and new folders with 0777, less the
umask; with the common umask 022 that is 0644 and 0755. This includes
`--dest` and any missing parents. A replaced or overwritten file is a new
file with these modes; it does not keep the old file's mode. Folders that
already exist, and adopted files, keep theirs. On Windows the mode bits do
not apply: new files and folders get the ACLs they inherit from their
folder.

Only regular files are copied. Symbolic links and other special entries on
the share are listed but neither followed nor copied.

Sync and delete keep one file of their own at the top of `--dest`: the
[folder lock](selection-and-history.md#the-folder-lock), `.seestar-wormhole.lock`, which is empty
and stays there. A share entry at the top of the share with that name, in
any letter case that the [name clash](#name-clashes) rules treat as the
same, is never copied, adopted or recorded, and neither is anything under a
folder of that name, whatever the selection says. Each pass logs one
`reserved name` warning for them, and they don't count as failed. `find`
and `browse` still list them.

### Selection

Sync copies the whole share, except files and folders that an exclude pattern
matches; with `--include`, it copies only what an include selects.
`--exclude`, `--include` and `--no-default-excludes` follow the shared
[selection rules](selection-and-history.md#selection), which list the
[built-in patterns](selection-and-history.md#built-in-patterns) and the
[pattern rules](selection-and-history.md#pattern-rules).

Each pass logs a `skipped` line for each excluded file or folder, with the
pattern that matched, and counts them in its summary; a folder has one line,
and what is in it has none. Files and folders that no include selects have no
line of their own; each pass counts them as `unmatched` in its summary, a
folder that is not listed once, without what is in it. An entry that an
exclude matches is logged and counted as `skipped` instead, whether or not an
include would select it. A pass whose listing fails still logs and counts
what it skipped, and what it found unmatched, before the failure.

### Dry run

`--dry-run` runs one pass that changes nothing on disk, then exits as
`--once` does. It connects, lists the share, and decides each file as a
real pass would, from the [state file](#the-state-file) and the destination
if they exist. It logs a line for each file it would copy, replace or adopt,
or that is waiting, a `file failed` line for each file that would fail, and a
`dry run done` summary; see [logs](#logs). Files that are done have no line.
It also logs a `would skip` line for each excluded file or folder, with the
pattern that matched, and a line for each top-level folder that has no copied
files recorded under it, so new folders stand out before a real pass.
`--once` may be given with it; `--interval` may not.

A dry run creates nothing, not even a missing destination or state folder,
and never moves the old default history folder: while only `seestar-copy`
exists, it reads the history there; see [location](selection-and-history.md#location).
It takes no [lock](selection-and-history.md#locks), removes no [temporary files](#temporary-files),
and leaves a torn final line in the state file in place; it logs that line
and ignores it. It works on a read-only destination and state, and may run
while another sync uses the same destination. Like any pass, it only reads
from the telescope.

It decides as the first pass of a new process does (see
[readiness](#readiness)), so it predicts the next `--once` run. The later
passes of a sync that is already running may treat files that have not
settled differently.

Like a pass, it looks only at files the device lists now; see
[what adoption covers](#what-adoption-covers).

It exits 0, or 1 if the connection or a listing fails or any file would
fail, as `--once` does.

## How a pass decides

For each regular file it lists, a pass looks at the device's size and mtime,
the listing of this process's previous pass, and the state file. These rules
apply in order:

1. **Name fails.** A name that cannot be stored, or that clashes with another,
   fails. See [names that fail](#names-that-fail).
2. **Done.** The state has the file with the same size and mtime. Nothing
   happens, and the destination is not looked at. A file you moved or deleted
   from the destination stays done and is not copied again.
3. **Waiting.** The file is not [ready](#readiness) yet. It is counted as
   `waiting` and looked at again next pass.
4. **Replace.** The state has the file, but the device's size or mtime has
   changed since. The file is downloaded again and recorded as `replaced`. It
   replaces the local file if that is still there, and is written again if
   you had moved it away.
5. **Adopt.** The state does not have the file, and a regular file with the
   same size and an mtime within 2 seconds of it is already at
   `<dest>/<path>`. Sync hashes the local file and records it as `adopted`,
   with no download. This is the only time the destination is consulted.
6. **Copy.** Otherwise the file is downloaded and recorded as `copied`. A
   local file at that path with a different size, or an mtime more than 2
   seconds off, is overwritten, since the device's copy is authoritative; the
   log line then has `overwrote=true`.

If something other than a regular file, such as a folder or a symbolic link,
is at `<dest>/<path>` when a file not in the state is ready, that file fails.

Adoption lets sync take over a folder that another tool, such as rclone,
filled, as long as that tool kept the device's mtimes. The 2 seconds allow
for filesystems that store mtimes coarsely, so a copy on a FAT32 or exFAT
drive is adopted too. The record keeps the device's mtime.

### What adoption covers

Adoption covers only files that the device lists now and that the pass
selects. Pass counts come from the current device listing, not from an
inventory of the destination. The skipped and unmatched counts also include
files and folders the selection leaves out. A local file with no counterpart
on the device, such as one the telescope no longer has, is never read,
counted or reported. A destination seeded from an older archive can
therefore show far fewer adopted files than it holds. That is expected.

### Readiness

The telescope writes some files over minutes, so sync waits until a file has
settled:

- its device mtime must be at least `--ready-age` old; and
- after the first pass, it must have been in the previous pass's listing with
  the same size and mtime. A new file therefore waits at least one
  `--interval`.

The first pass of a process has no previous listing. There, a file counts as
settled only if its mtime is also at least `--first-pass-age` old. Restarting
sync starts again from a first pass. A pass that fails before its listing
completes does not count.

A file whose device mtime is in the future, by the host's clock, is not ready.
If the telescope's clock runs ahead of the computer's, new files wait until
the computer's clock passes their mtime plus `--ready-age`. If it runs
behind, new files look older than they are, but after the first pass the
check against the previous listing still makes them wait.

### A failed pass

If the connection fails, or any folder that is not excluded cannot be listed,
including the share root, the pass fails: nothing is copied, and the error is
logged. With `--once` or `--dry-run`, sync exits 1;
otherwise it tries another pass after `--interval`.

A file that fails is logged and left for the next pass; the other files go on.
There is one attempt per file per pass, and no retry loop within a pass.

A pass after the first that finds the [folder lock](selection-and-history.md#the-folder-lock) held
by another sync or a delete is skipped: it logs `pass skipped` with
`reason=folder_busy`, connects to nothing, and sync tries again after
`--interval`. If `--dest` is removed while sync runs, each later pass fails
before it connects, logging `pass failed` with
`reason=destination_missing`, and sync goes on without creating the folder
again. Restart sync to copy into a folder created again at that path.

## Copying a file

Up to `--workers` files are copied at once, over the pass's one session. Each
download:

1. creates the file's folder if needed;
2. writes to a [temporary file](#temporary-files) in that folder, computing
   the SHA-256 as it goes;
3. checks that exactly the listed size was read; a shorter or longer file
   fails;
4. fsyncs and closes the temporary file, and sets the device's mtime on it;
5. renames it over the final path, then fsyncs the folder (not on
   [Windows](#windows));
6. appends the file's line to the state file and fsyncs it.

A crash or failure at any step leaves either no state line for the file, or
the complete file under its final name. If a crash loses a state line, the
next pass finds the complete file and adopts it.

An adoption only reads the local file. It fails if, while the file is being
hashed, its size changes, its mtime moves more than 2 seconds from the
device's, or something else takes its path. A rewrite that keeps the size and
a near mtime is not noticed.

### Temporary files

A download is written to `.seestar-copy-<hash>.tmp` in the same folder as its
final file, where `<hash>` is 64 lowercase hex digits computed from the
file's path on the share. The leading dot hides it from most file browsers,
and the name never ends in `.fit`, so a `*.fit` pattern never matches a
partial file.

A failed or stopped download removes its temporary file. At the start of
each pass, after taking the [folder lock](selection-and-history.md#the-folder-lock), sync removes
every leftover temporary file anywhere under the destination: only regular
files with exactly that form of name, and never through a symbolic link. It
does not resume partial downloads. A folder under the destination that sync
is not allowed to read is skipped with a warning; any other error reading
the destination stops sync with exit 1 at the first pass, and fails a later
pass.

## The state file

The state file is how sync remembers what it has copied, one per
destination, in the [copy history](selection-and-history.md#copy-history).
Its [location](selection-and-history.md#location) and
[format](selection-and-history.md#format) are shared with the app's Copy. It
belongs to sync; don't edit it while sync is running.

Sync holds the [history lock](selection-and-history.md#the-history-lock) for
the whole run and the [folder lock](selection-and-history.md#the-folder-lock)
during each pass. If another copy or a delete holds one,
[the first pass exits 1 and a later pass is skipped](selection-and-history.md#when-the-other-side-holds-a-lock).

## Stopping

Sync runs until it receives Ctrl+C (SIGINT) or SIGTERM, or until the pass of
`--once` or `--dry-run` ends. On a signal:

- reads in progress stop, and their temporary files are removed;
- files not yet started are not started;
- a file already renamed into place is still recorded;
- the session logs off, a `pass stopped` line is logged, and sync exits 0.

Files cut off by the stop are counted as `failed` in the `pass stopped` line,
without a line each. A signal during the wait between passes exits 0 at once.
Only complete files are in the state, so the next run carries on where this
one stopped.

## Logs

Sync logs to stderr, as text lines with UTC timestamps to the millisecond, or
as JSON records with the same messages and keys with `--log-format json`.
Help and `licenses` write to stdout. Per-file lines are logged as
each file finishes. A pass that skips the `log` folder and copies two files
looks like this:

```text
time=2026-11-02T03:10:00.118Z level=INFO msg=skipped path=log exclude=/log
time=2026-11-02T03:10:01.620Z level=INFO msg="file copied" path="MyWorks/M 31/light_002.fit" size=16000000 elapsed=1.497s
time=2026-11-02T03:10:03.290Z level=INFO msg="file copied" path="MyWorks/M 31/light_003.fit" size=16000000 elapsed=1.668s
time=2026-11-02T03:10:03.502Z level=INFO msg="pass done" copied=2 replaced=0 adopted=0 waiting=1 failed=0 skipped=1 unmatched=0 bytes=32000000 elapsed=3.502s rate=8.7MiB/s
```

The format is logfmt-like `key=value` pairs, as Go's `log/slog` text handler
writes them. A value that is empty, or that contains a space of any kind, a
`"`, an `=` or a character that does not print, is double-quoted with Go
string escaping, such as `\"` for a quote inside it. In the example above
that gives `msg="file copied"` and `path="MyWorks/M 31/light_002.fit"`. Read
the lines with a logfmt parser, not by splitting on spaces.

The messages and keys below are what this release logs. They are not part of
the [versioning contract](../versioning.md#not-in-the-contract); a release that
changes them says so in its notes.

A failure record that ends a pass, a dry run, a listing, a deletion, a
browse login or request, or a command also carries four classified keys,
in both formats:

- `source`: the tool, `copy` (sync), `find`, `browse` or `delete`.
- `op`: what failed: `pass`, `preview` (a dry run), `list`, `connect`,
  `records` (browse reading the state), `read`, `delete`, or `run` for
  the command's own `<command> failed` record.
- `phase`: the step that failed, such as `connect`, `list` or `open`.
- `reason`: the class of the failure, such as `connection_refused`,
  `timeout`, `permission_denied`, `not_found` or `operation_failed`.

A destination whose history or [folder lock](selection-and-history.md#the-folder-lock) another sync
or a delete holds reads `phase=open reason=busy`, as it does in the app.

`error` may name paths, hosts and files, while `phase` and `reason` never do.
For example:

```text
time=2026-11-02T03:00:00.000Z level=ERROR msg="pass failed" source=copy op=pass phase=connect reason=connection_refused copied=0 ... error="connect to 127.0.0.1:1: ..."
```

| Level | Message | Keys | When |
| --- | --- | --- | --- |
| INFO | `file copied` | `path`, `size`, `overwrote=true` when it replaced a different local file, and `elapsed`, the file's download time | A file was downloaded and recorded. |
| INFO | `file replaced` | `path`, `size`, `elapsed` | A file that changed on the device was downloaded again. |
| INFO | `file adopted` | `path`, `size` | A file already in the destination was hashed and recorded. |
| WARN | `file failed` | `path`, `reason` | A file failed this pass; it is tried again next pass. |
| INFO | `skipped` | `path`, `exclude` | The file or folder matches the pattern `exclude`, the first one that matched, and is left out, by a pass or by `find`. A folder has one line; what is in it is not listed. See [selection](#selection). |
| INFO | `pass done` | see below | A pass finished. |
| ERROR | `pass failed` | `source`, `op`, `phase`, `reason`, see below, and `error` | The connection or a listing failed. |
| WARN | `pass failed` | `reason=destination_missing`, `dest` | `--dest` was removed while sync runs; see [a failed pass](#a-failed-pass). |
| WARN | `pass skipped` | `reason=folder_busy`, `dest`, `error` | Another sync or a delete holds the [folder lock](selection-and-history.md#the-folder-lock) at a pass after the first. |
| WARN | `reserved name` | `path`, the first such entry; `entries`, how many; `reason` | Top-level share entries named `.seestar-wormhole.lock` were left out; see [destination layout](#destination-layout). |
| INFO | `pass stopped` | see below | A signal stopped the pass. |
| INFO | `would copy` | `path`, `size`, and `overwrite=true` when it would replace a different local file | Dry run: the file would be downloaded and recorded as `copied`. |
| INFO | `would replace` | `path`, `size` | Dry run: the file changed on the device and would be downloaded again. |
| INFO | `would adopt` | `path`, `size` | Dry run: the file is already in the destination and would be hashed and recorded. |
| INFO | `would wait` | `path`, `size` | Dry run: the file is not ready yet. |
| INFO | `would skip` | `path`, `exclude` | Dry run: the same as `skipped`. |
| INFO | `no copied files recorded under this top-level folder` | `folder` | Dry run: the state has no file under this folder at the share root, such as a folder a new release now covers. |
| INFO | `dry run done` | `copy`, `replace`, `adopt`, `waiting`, `failed`, `skipped`, `unmatched`, `bytes`, `elapsed` | A dry run finished. Counts other than `unmatched` are of the lines above and of `file failed` lines. `unmatched` counts entries left out by includes, without a line of their own; `bytes` is what it would download. |
| ERROR | `dry run failed` | `source`, `op`, `phase`, `reason`, as `dry run done`, and `error` | The connection or a listing failed in a dry run. |
| INFO | `dry run stopped` | as `dry run done` | A signal stopped a dry run. |
| WARN | `dropped the torn final line of the state file` | `file`, `line`, `reason` | See [format](selection-and-history.md#format). |
| WARN | `ignored the torn final line of the state file` | `file`, `line`, `reason` | The same, in a [dry run](#dry-run), which leaves the file as it is. |
| WARN | `skipped a folder that cannot be read while removing leftover temporary files` | `folder`, `reason` | See [temporary files](#temporary-files). |
| WARN | `skipped a malformed state file while checking for another spelling of the destination` | `file`, `reason` | Another state file in the `sync` folder cannot be read. |
| ERROR | `sync failed` | `source`, `op=run`, `phase`, `reason`, `error` | Sync is exiting 1. |
| INFO | `listing done` | see [find logs](#find-logs) | A `find` listing finished. |
| ERROR | `listing failed` | `source`, `op`, `phase`, `reason`, as `listing done`, and `error` | The connection or a listing failed in `find`. |
| ERROR | `find failed` | `source`, `op=run`, `phase`, `reason`, `error` | Find is exiting 1. |
| ERROR | `sync failed`, `find failed`, `browse failed`, `delete failed` or `scan failed` | `option`, `rule`, `help`, `phase=arguments`, `reason=invalid_option` | A usage error; the command is exiting 2. It has no `source` or `op`; `phase=arguments` is only on this record. |
| INFO | `moved the copy history` | `from`, `to` | With the default `--history-dir`: the history folder of earlier releases, `seestar-copy`, was moved to `copy-history`; see [location](selection-and-history.md#location). Logged once, by the first sync or delete that isn't a dry run. Find, browse and dry runs never move it. |
| INFO | `saved settings` | `profile`, `filled` | With `--profile`: the flags it filled, by name, without their values; see [use the app's saved settings](#use-the-apps-saved-settings). Logged by sync, find, browse and delete. |
| INFO | `found the telescope at another address on this network` | `telescope`, `saved`, `endpoint`, `addresses`, `other` when another serial answered at the saved address, `replies` | With `--profile default` on this network: a connect's scan found the telescope at an address or port other than the saved one, and it connected there; see [use the app's saved settings](#use-the-apps-saved-settings). `telescope` is its name, model and serial as the reply gave them, such as `S50_Garden (Seestar S50, serial 4ab1c2d3)`; `saved` is the saved address, `endpoint` the one it connected to, `addresses` where it answered, and `other` the telescope that answered at the saved address. Logged again only when the outcome changes. |
| WARN | `the telescope wasn't seen in a scan of this network; tried its saved address` | `serial`, `saved`, `replies` | As above, but no reply carried the saved serial, so the saved address was dialed, whether or not it connected. Logged again only when the outcome changes. |
| WARN | `another telescope answers at the saved address, and a scan of this network didn't find the saved one` | `serial`, `saved`, `other`, `replies` | As above, but another serial answered from the saved address, which isn't used, and no reply carried the saved serial; the connect fails. `other` names the telescope that answered there. |
| WARN | `found the telescope on this network, but it didn't connect where it answered` | `telescope`, `saved`, `addresses`, `other` when another serial answered from the saved address, `replies` | As above, but no address that answered with the serial connected, or another serial answered at each of them too; the connect fails. |
| WARN | `diagnostics_lost` | `rule`, `count`, `bytes` | Stderr could not take every line, and the lines were dropped rather than stall the command. Written once stderr takes lines again; the command exits 1. |

Each pass that runs ends with one summary line, whose keys are below. A later
check skipped for `folder_busy` or ended by `destination_missing` emits only the
WARN record listed above, without summary counts.

| Key | Meaning |
| --- | --- |
| `copied` | Files downloaded and recorded as `copied`. |
| `replaced` | Files downloaded again and recorded as `replaced`. |
| `adopted` | Files hashed in place and recorded as `adopted`. |
| `waiting` | Files not ready yet. They have no line of their own. |
| `failed` | Files that failed, each with its own `file failed` line, plus files cut off by a stop. |
| `skipped` | Files and folders an exclude left out, each with its own `skipped` line. What is in a skipped folder is not counted. |
| `unmatched` | Files and folders that no `--include` selects and no exclude matches. They have no line of their own. What is in a folder that is not listed is not counted. Always 0 without `--include`. |
| `bytes` | Bytes downloaded: the sizes of the copied and replaced files. |
| `elapsed` | The pass's duration, as a Go duration such as `3.502s` or `2m5.1s`. |
| `rate` | `bytes` over `elapsed`, in MiB/s, such as `8.7MiB/s`. Left out when nothing was downloaded. |

Files that are done are not counted. A usage error is a single line naming the
flag and the rule it broke, and the help command to run. It is a text line
whatever `--log-format` says, since that flag may be the bad one:

```text
time=2026-11-02T03:00:00.000Z level=ERROR msg="sync failed" option=--workers rule="must be 1 to 64" help="seestar-wormhole-cli sync --help" phase=arguments reason=invalid_option
```

Flag values are never echoed in it. A flag that belongs to another command is
named, with that command's help; any other unknown flag is reported as
`option=arguments rule="unknown flag"`.

Stderr never holds a command up: a stalled terminal or log reader loses lines
instead, so Ctrl+C still stops sync. The file commands never write log lines
to stdout, which is find's listing and browse's URL, so lost lines are only
counted, in a `diagnostics_lost` line when stderr recovers.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Stopped by a signal; a `--once` or `--dry-run` pass with no failed files; or help printed. |
| 1 | A runtime failure: the destination or state could not be opened or locked (including another copy or delete holding the [folder lock](selection-and-history.md#the-folder-lock) at the first pass), leftover temporary files could not be removed, a `--once` or `--dry-run` pass failed (`connect to ...` or `list the share: ...`), a `--once` or `--dry-run` pass had failed files (`failed files: N`), output could not be written, or log lines were lost (`diagnostics_lost`). |
| 2 | A usage error: an unknown or invalid flag, including `--state-dir` and `--version`, a missing `--endpoint` or `--dest`, an empty `--history-dir`, `--dry-run` with `--interval`, a positional argument, a refused `--profile`, or a bad `--scan`. |

Without `--once` or `--dry-run`, failed passes and failed files do not end
sync: it logs them and tries again on the next pass.

Lost log lines and the usage errors above end every file command the same
way. For `find`, see [find exit codes](#find-exit-codes); for `delete`, see
[delete exit codes](#delete-exit-codes); for `browse`, see
[browse exit codes](#browse-exit-codes).

## Names that fail

Sync never renames a file. A name it cannot store fails with a `file failed`
line naming the rule, on every pass, and the other files go on. With `--once`
or `--dry-run` such a file makes the exit code 1. To leave it out, add an
[`--exclude`](#selection) pattern that matches it or its folder.

### On every platform

A path from the share fails if it:

- is not valid UTF-8 (the state file is JSON and could not record it);
- contains a NUL character;
- is absolute, has a backslash, starts with a drive letter such as `C:`, or
  has an empty, `.` or `..` element;
- has a file name of the form `.seestar-copy-<64 hex digits>.tmp`, in any
  case, which is reserved for [temporary files](#temporary-files);
- has a negative size, or an mtime the state file cannot record (zero, or a
  year outside 0 to 9999);
- appears twice in one listing.

### On Windows

When sync runs on Windows, a path also fails if any element:

- contains `:`, `*`, `?`, `"`, `<`, `>` or `|`, or a control character
  U+0001 to U+001F;
- ends in a dot or a space;
- is a reserved device name, in any case and with or without an extension:
  `CON`, `PRN`, `AUX`, `NUL`, `CONIN$`, `CONOUT$`, `COM0` to `COM9` and
  `LPT0` to `LPT9` (including `COM¹`, `COM²`, `COM³`, `LPT¹`, `LPT²` and
  `LPT³`).

On Linux and macOS these names are copied as they are. A destination on a
Windows-formatted disk mounted on Linux or macOS may still refuse them; that
file then fails when it is written.

### Name clashes

Windows, macOS by default, and some Linux mounts treat names that differ only
in case or Unicode normalization as the same name. Two such paths would land
on one local file, so on every platform sync compares each path, one element
at a time, after Unicode NFC normalization and case folding. If two listed
paths, or a listed path and one in the state, or a file and a folder (`A` and
`a/b.fit`), match that way, every spelling fails on every pass:

```text
name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization
```

No spelling is picked over the other, nothing is adopted, and the state is
left alone. Because paths in the state count too, a file renamed on the device
to another case of a name sync has already recorded keeps failing. The names
are still copied byte for byte; the folded form is used only to compare.

## Windows

- **Folder fsync.** Go cannot fsync a folder on Windows, so step 5 of
  [copying a file](#copying-a-file) skips it there. After a power loss or a
  system crash, a file whose state line was written may be missing, or left
  under its temporary name (which the next start removes). Its state line says
  it is done, so sync does not copy it again.
  [Deleting the state file](selection-and-history.md#format) makes sync copy every missing file again.
- **Rename over an open file.** Replacing or overwriting a local file fails
  if another program, such as an image viewer, has it open without allowing
  deletion. That file fails for the pass and is tried again on the next.
- **Names.** See [on Windows](#on-windows).

## find

```sh
seestar-wormhole-cli find --endpoint HOST:PORT [flags]
```

Lists the files and folders on the telescope's share, once, with one line per
entry on stdout. It selects what a sync pass would list, and with `--dest` it
says for each file whether that destination's [state](#the-state-file) has
it. Like sync, it only reads from the telescope. It writes nothing on disk:
not the destination, not the state, not a lock.

```sh
seestar-wormhole-cli find --endpoint 127.0.0.1:4445 --include /MyWorks
seestar-wormhole-cli find --endpoint 127.0.0.1:4445 --dest ~/Seestar --older-than 7d
seestar-wormhole-cli find --endpoint 127.0.0.1:4445 --format jsonl > share.jsonl
```

### find flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | none; required | As for sync. See [endpoint](#endpoint). |
| `--exclude`, `--no-default-excludes`, `--include` | as for sync | The same [selection](#selection) as sync, with the same built-in patterns. |
| `--older-than` | no limit | List only files whose device mtime is at least this old. See below. |
| `--format` | `text` | `text` or `jsonl`. See [text format](#text-format) and [JSONL format](#jsonl-format). |
| `--dest` | none | A destination whose state gives each file a [status](#statuses). It is never created, and need not exist. It must not be empty when given. |
| `--history-dir` | as for sync | The folder that holds the state file of `--dest`; see [location](selection-and-history.md#location). Only with `--dest`, and it must not be empty when given. |
| `--profile` | none | As for sync: `default` fills the destination, history, endpoint and selection not given. |
| `--scan` | as for sync | As for sync. |
| `--log-format`, `--log-level` | as for sync | As for sync. See [logs](#logs). |

`--older-than` takes a Go duration, such as `36h` or `90m`, or a whole number
of days, such as `7d`. The two do not mix: `1d12h` is a usage error, and so
is a negative value. A file is old enough when the time since its device mtime
is at least the value; a file whose mtime is in the future never is. `0` is no
limit. Folders are listed whatever their age.

All flags are checked before anything touches the network or the disk, and a
bad one exits 2.

### What find lists

Find lists every folder the selection reads and every regular file it selects,
in listing order: a folder comes before what is in it, and the names in each
folder are in byte order. A folder that an exclude matches is left out with
everything in it, and so is a folder that no `--include` could match. A folder
is listed even if nothing in it is selected. Entries that are neither a
regular file nor a folder, such as links, are not listed; sync ignores them
too.

Find logs a `skipped` line for each excluded file or folder, as a pass does,
and ends with a `listing done` line; see [find logs](#find-logs). The listing
is written only once the whole share has been listed, so a listing that fails
writes nothing to stdout.

### Text format

`--format text`, the default, writes one line per entry, ending in a line
feed, with fields separated by one space:

```text
TYPE SIZE MTIME PATH
TYPE SIZE MTIME STATUS PATH     (with --dest)
```

| Field | Meaning |
| --- | --- |
| `TYPE` | `d` for a folder, `-` for a file. |
| `SIZE` | The size in bytes that the server lists, in decimal. For a folder it means nothing, and is usually `0`. |
| `MTIME` | The device mtime, in UTC, in RFC 3339 with as many fractional digits as it needs and no trailing zeros, such as `2026-11-01T03:09:58.1234567Z` or `2026-11-01T02:14:07Z`. |
| `STATUS` | Only with `--dest`: the file's [status](#statuses), or `-` for a folder. |
| `PATH` | The share path, with `/` between names. |

`PATH` is written as it is, unless it holds a space, a `"`, a `\`, a
character that is not printable, or bytes that are not UTF-8. Then it is
written as a double-quoted string in Go's syntax: `\"` and `\\`, `\t`, `\n`
and the other C escapes, `\xHH` for a byte that is not UTF-8, and `\uHHHH`
or `\UHHHHHHHH` for other characters that are not printable. So the first
three fields (four with `--dest`) never contain a space, the path is the rest
of the line, and a path that starts with `"` is quoted. Go's `strconv.Unquote`
reverses it. To read paths without Go, use the JSONL format.

```text
$ seestar-wormhole-cli find --endpoint 127.0.0.1:4445 --include /MyWorks --dest ~/Seestar
d 0 2026-11-01T02:14:07Z - MyWorks
d 0 2026-11-01T03:10:02.5Z - "MyWorks/M 31"
- 734512 2026-11-01T03:10:02.5Z new "MyWorks/M 31/M 31.jpg"
- 16000000 2026-11-01T03:09:58.1234567Z copied "MyWorks/M 31/light_001.fit"
- 16000000 2026-11-01T03:10:00.1Z changed "MyWorks/M 31/light_002.fit"
```

### JSONL format

`--format jsonl` writes one JSON object per entry, one per line, ending in a
line feed, with these members in this order:

| Member | Type | Meaning |
| --- | --- | --- |
| `path` | string | The share path, as in the text format, but as a JSON string. |
| `type` | string | `dir` for a folder, `file` for a file. |
| `size` | number | As `SIZE` in the text format. |
| `mtime` | string | As `MTIME` in the text format. |
| `status` | string | Only with `--dest`, and only for a file: its [status](#statuses). |

Strings follow JSON's rules; `<`, `>` and `&` are written as they are. Names
from the share are valid UTF-8; a byte that is not would be written as
U+FFFD. A later release may add members, so ignore the ones you do not know.

```text
{"path":"MyWorks","type":"dir","size":0,"mtime":"2026-11-01T02:14:07Z"}
{"path":"MyWorks/M 31","type":"dir","size":0,"mtime":"2026-11-01T03:10:02.5Z"}
{"path":"MyWorks/M 31/M 31.jpg","type":"file","size":734512,"mtime":"2026-11-01T03:10:02.5Z","status":"new"}
{"path":"MyWorks/M 31/light_001.fit","type":"file","size":16000000,"mtime":"2026-11-01T03:09:58.1234567Z","status":"copied"}
{"path":"MyWorks/M 31/light_002.fit","type":"file","size":16000000,"mtime":"2026-11-01T03:10:00.1Z","status":"changed"}
```

### Statuses

With `--dest`, find reads that destination's state file as a
[dry run](#dry-run) does, and compares each file with its record as a pass
does:

| Status | Meaning |
| --- | --- |
| `copied` | The state has a record for the path with the device's size and mtime. Sync will not copy it again. |
| `changed` | The state has a record for the path, but the size or mtime differs. Sync would download it again. |
| `new` | The state has no record for the path. Sync would copy or adopt it once it is ready. |

`copied` says what the state records, not that the file is still in the
destination: a file moved out of it is still `copied`. Records whose files
are no longer on the share are not listed. Folders have no status.

Find takes no [lock](selection-and-history.md#locks), so it may run while a sync uses the same
destination. A missing or empty state file makes every file `new`. If the
destination does not exist, find still uses any existing state. It creates
neither the destination nor the state folder. A torn final line in the state
file is logged and ignored, as in a dry run. Without `--dest`, no state is
read.

### find logs

Find logs to stderr as sync does: a `skipped` line for each excluded file or
folder, then one summary line, `listing done`, or `listing failed` with an
`error` key, followed by `find failed`:

```text
time=2026-11-09T09:00:00.118Z level=INFO msg=skipped path=log exclude=/log
time=2026-11-09T09:00:01.502Z level=INFO msg="listing done" files=212 folders=9 bytes=3518046720 recent=0 skipped=3 unmatched=0 copied=200 changed=1 new=11 elapsed=1.502s
```

| Key | Meaning |
| --- | --- |
| `files` | Files listed. |
| `folders` | Folders listed. |
| `bytes` | The sizes of the files listed. |
| `recent` | Files that `--older-than` left out. Always 0 without it. |
| `skipped` | Files and folders an exclude left out, each with its own `skipped` line, as in a pass. |
| `unmatched` | As in a pass: files and folders that no `--include` selects. |
| `copied`, `changed`, `new` | Only with `--dest`: files listed with each [status](#statuses). |
| `elapsed` | The time the listing took. |

### find exit codes

| Code | Meaning |
| --- | --- |
| 0 | The listing is complete. |
| 1 | The state could not be read, the connection or a listing failed (`connect to ...` or `list the share: ...`), the output could not be written, or a signal stopped find before it wrote the whole listing. |
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint`, a bad `--format` or `--older-than`, an empty `--dest` or `--history-dir`, `--history-dir` without `--dest`, a positional argument, a refused `--profile`, or a bad `--scan`. |

## browse

```sh
seestar-wormhole-cli browse --endpoint HOST:PORT [--dest DIR] [--listen ADDR] [--open] [selection flags]
```

Serves a read-only web page for looking through the telescope's share. The
page lists one folder at a time, shows a `.jpg`, a `.txt` or a FITS header
when you click it, and saves any file to this computer through the browser. With `--dest`, each file
shows its [status](#statuses) in that destination's state, as for find, and
a file or folder that the [selection](#selection) leaves out shows
[`left out`](#left-out-in-browse).
Like sync and find, browse only reads from the telescope, and it writes
nothing on disk: not the destination, not the state, not a lock.

```sh
seestar-wormhole-cli browse --endpoint 127.0.0.1:4445 --open
seestar-wormhole-cli browse --endpoint 127.0.0.1:4445 --dest ~/Seestar
```

Once it is listening, browse prints exactly one line to stdout, the page's
URL, and serves until Ctrl+C or SIGTERM:

```text
http://127.0.0.1:53817/
```

For `--listen` `0.0.0.0` or `::`, the URL uses `127.0.0.1` or `[::1]`, with
the port browse listens on.

### browse flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | none; required | As for sync. See [endpoint](#endpoint). |
| `--listen` | `127.0.0.1:0` | The IP address and port to serve the page on. Port `0` picks a free port. See below. |
| `--open` | off | Also open the page in the default browser. The URL is printed either way. |
| `--dest` | none | As for find: a destination whose state gives each file a [status](#statuses). It is never created, and need not exist. It must not be empty when given. |
| `--history-dir` | as for sync | As for find: the folder that holds the state file of `--dest`. Only with `--dest`, and it must not be empty when given. |
| `--profile` | none | As for sync: `default` fills the destination, history and endpoint not given, and the selection with a destination. |
| `--scan` | as for sync | As for sync. |
| `--log-format`, `--log-level` | as for sync | As for sync. See [logs](#logs). |
| `--exclude`, `--no-default-excludes`, `--include` | as for sync | The [selection](#selection) whose left-out files and folders show [`left out`](#left-out-in-browse). Only with `--dest`. |

Browse always shows the whole share. The selection flags only choose what
shows as `left out`, and with no flags the built-in patterns do.

`--listen` takes an IP address and a port, such as `127.0.0.1:0`,
`[::1]:8080`, `192.168.1.5:0` or `0.0.0.0:0`, never a host name, not even
`localhost`. An IPv4-mapped IPv6 address, such as `[::ffff:192.168.1.5]:0`,
and an address with a zone, such as `[fe80::1%en0]:0`, are refused too. Use
the plain IPv4 address for a mapped address. An IPv6 address with a zone is
not supported; choose another address, such as `[::1]:0` or an available IPv4
address.

Loopback addresses, such as the default, serve this computer only. A
non-loopback address, including `0.0.0.0` or `::`, serves the share without a
password to anyone who can reach it, and browse logs a WARN line,
`serving the share unauthenticated to the network`, when it starts.

All flags are checked before anything touches the network or the disk, and a
bad one exits 2.

### The page

The page shows the endpoint, the path of the folder as links back to each
parent, and one row per folder and regular file in it, folders first, with a
preview, the name, the size, the modification time, the status with
`--dest`, and a Save link for each file. Links and other kinds of entries
are not shown, as find does not list them. Click a folder to open it; the
browser's Back button returns to the previous folder.

#### Left out in browse

With statuses, an entry that the selection leaves out shows `left out`
instead of `copied`, `changed` or `new`: a sync pass would not copy it,
whatever the state records. That is a file or folder an exclude matches,
anything inside such a folder, and, with includes, whatever no include
selects, by the same rules a pass uses. Folders show only `left out`; a
folder the selection keeps has no status. A key under the table lists
each status's badge with what it means.

A file's preview is the telescope's own small image: for a file `S.ext`,
`S_thn.jpg` in the same folder if there is one, and otherwise `S.jpg`. Browse
renders no FITS image, plays no video and processes no image.

Clicking the name or the preview of a file opens a viewer with its own Save
link for these names, matched in any case:

| Name ends in | Clicking it shows |
| --- | --- |
| `.jpg` | The image. |
| `.txt` | Its text, read as UTF-8. For a file over 1 MiB, only the first 1 MiB is shown, with a note; save the file to see all of it. |
| `.fit` or `.fits` | Its FITS primary header as text, one card per line, up to the `END` card, without blank cards. Browse reads at most the first 28,800 bytes (ten 2,880-byte blocks); if no `END` card is in them, it shows what it read, with a note. The image itself is never shown. |

The browser is given a `.jpg` as `image/jpeg` and a `.txt` as
`text/plain; charset=utf-8`. Every other file, whatever its name, including
a FITS file, is sent as a download, never shown by the browser itself; the
page reads a FITS header on its own. Save links save the file under its name
on the share, through the browser's own download.

Each listing reads the folder from the telescope again, and with `--dest`
reads the state again whenever the state file's size or modification time
has changed, so the statuses follow a sync running at the same time. Browse takes no [lock](selection-and-history.md#locks). A file still being written can be shown
or saved torn; open it again once it has stopped changing.

A failed request never stops browse. A telescope that cannot be reached, a
listing or read that fails and a state file that fails to load are shown on
the page, and the next click tries again. A download that breaks partway
fails in the browser.

### The device session

Browse logs in to the share on the first request, uses that one session for
every request, and logs off after 30 seconds without one. An SMB error
other than a missing path, a refused access or the wrong kind of entry
closes the session, and so does a request
whose device call is still running 5 seconds after its browser went away.
The next request logs in again. A login that takes longer than 30 seconds
fails. None of these times is a flag.

### Who can use the page

Browse has no password or token. The page and everything it reads are open
to:

- **Any program and any account on this computer**, through the loopback
  port. That gives them nothing new on the share, which they can reach as
  guest through the same endpoint, but with `--dest` they also see which
  files the state records as copied.
- **Anyone on the network who can reach the address**, when `--listen` is
  not a loopback address. They get the same read-only access to the share
  and the statuses. Browse speaks plain HTTP, as the telescope's SMB guest
  access is unencrypted too.

Browse answers only requests whose `Host` is the address it listens on, or,
for `0.0.0.0` or `::`, any IP address with its port. A host name is
refused, so a web site cannot reach browse by pointing its own name at
`127.0.0.1`. A browser that sends Fetch Metadata marks a request that comes
from another web site, and browse refuses it with 403 before it touches the
telescope or the state, on every route, the page included. Browse sends no
CORS headers, so another site's script cannot read its answers, and it sends
`Cross-Origin-Resource-Policy: same-origin`, so the browser does not hand a
file to another site's page.

When Fetch Metadata is absent, including on ordinary HTTP LAN URLs and in
older browsers, cross-site requests can reach the share if other browser
restrictions permit them. SOP and no CORS prevent script access to response
bodies. CORP additionally blocks cross-origin no-cors resource delivery in
browsers that implement it. Browsers lacking CORP can expose image load
results and dimensions, and response timing can expose information. Browse
does not promise zero blind reads or zero metadata leakage in this case.
Such requests can still change nothing: browse only reads.

A browser extension allowed on every site, and malware running as you, can
read the page as you do.

### Stopping browse

Ctrl+C or SIGTERM stops browse: it logs off the device, which ends any read
in progress, gives open requests a second to finish, closes the rest and
exits 0. A download cut off this way fails in the browser.

### browse logs

Browse logs to stderr as sync does. It logs no line per request, and none
for a refused request, so a hostile page cannot fill the log. An open with
`--dest` looks like this:

```text
time=2026-11-09T09:00:00.004Z level=INFO msg="browse listening" addr=127.0.0.1:53817 endpoint=127.0.0.1:4445 dest=/home/me/Seestar
time=2026-11-09T09:00:02.310Z level=INFO msg="session opened" endpoint=127.0.0.1:4445
time=2026-11-09T09:01:12.950Z level=INFO msg="session closed" endpoint=127.0.0.1:4445 reason=idle
```

| Level | Message | Keys | When |
| --- | --- | --- | --- |
| INFO | `browse listening` | `addr`, `endpoint`, and `dest` when given | Browse is serving. |
| WARN | `serving the share unauthenticated to the network` | `addr` | At start, when `--listen` is not a loopback address. |
| INFO | `session opened` | `endpoint` | The login to the share succeeded. |
| INFO | `session closed` | `endpoint`, `reason` | The session logged off. `reason` is `idle` after 30 seconds unused, `failed` after an SMB error, `stuck` when a call outlived its request by 5 seconds, or `stopped` when browse stopped. |
| WARN | `connect failed` | `source`, `op`, `phase`, `reason`, `endpoint`, `error` | The login failed or took longer than 30 seconds. The requests waiting on it are answered with the error. |
| WARN | `request failed` | `source`, `op`, `phase`, `reason`, `path`, `error` | A listing or file read failed on the device, the state could not be read, or a download broke after it started. Not for a refused request, a path that does not exist, or a browser that went away. |
| WARN | `could not open a browser` | `error` | `--open` failed. Browse keeps serving; open the printed URL yourself. |
| WARN | `ignored the torn final line of the state file` | `file`, `line`, `reason` | As in find. |
| INFO | `browse stopped` | | A signal stopped browse. |
| ERROR | `browse failed` | `source`, `op=run`, `phase`, `reason`, `error` | Browse is exiting 1. |

### browse exit codes

| Code | Meaning |
| --- | --- |
| 0 | Stopped by a signal. |
| 1 | Browse could not start or keep serving: the address could not be listened on, the state of `--dest` could not be read when browse started, the URL could not be written to stdout, or the HTTP server stopped with an error. |
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint`, a bad `--listen`, an empty `--dest` or `--history-dir`, `--history-dir` or a selection flag without `--dest`, a bad pattern, a positional argument, a refused `--profile`, or a bad `--scan`. |

## delete

```sh
seestar-wormhole-cli delete --endpoint HOST:PORT --dest DIR [--force] [flags]
seestar-wormhole-cli delete --endpoint HOST:PORT --dest DIR --trust-state [--force] [flags]
seestar-wormhole-cli delete --endpoint HOST:PORT --ignore-state --include PAT [--force] [flags]
```

Deletes files from the telescope's share once they are copied, then removes
the folders the run emptied. It runs once, one file at a time.

delete is the only command that changes the telescope's share. Without
`--force` it only previews: it lists the share, logs what it would delete,
and changes nothing, exactly as `--dry-run` does. With `--force` it deletes,
and does not ask first. `--force` only means "act": it never skips or
weakens the proof. `--trust-state` and `--ignore-state` are the only ways to
ask for less proof.

```sh
seestar-wormhole-cli delete --endpoint 127.0.0.1:4445 --dest ~/Seestar
seestar-wormhole-cli delete --endpoint 127.0.0.1:4445 --dest ~/Seestar --force
seestar-wormhole-cli delete --endpoint 127.0.0.1:4445 --ignore-state --include '*.jpg' --older-than 30d --force
```

### delete flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | none; required | As for sync. See [endpoint](#endpoint). |
| `--dest` | none; required, except with `--ignore-state`, which refuses it | The destination whose [state](#the-state-file), and by default whose local copies, prove that a file was copied. It is never created. It must be the destination of **this** telescope; see [what the proof does not show](#what-the-proof-does-not-show). |
| `--history-dir` | as for sync | The folder that holds the state file of `--dest`; see [location](selection-and-history.md#location). Refused with `--ignore-state`, and it must not be empty when given. |
| `--profile` | none | As for sync, but `default` fills only the destination and history, not with `--ignore-state`, and the endpoint; never the selection, proof or age. |
| `--scan` | as for sync | As for sync. |
| `--log-format`, `--log-level` | as for sync | As for sync. See [logs](#logs). |
| `--trust-state` | off | Prove a file by its state record alone, without reading the local copy. Use it when the copies were moved out of `--dest` or the drive is offline. |
| `--ignore-state` | off | Read no state, and delete every selected file that is old enough, whether or not it was copied. Needs at least one `--include`. |
| `--older-than` | `7d` | Delete only files whose device mtime is at least this old. The grammar is find's: a Go duration, such as `36h`, or a whole number of days, such as `7d`, not both, and not negative. `0` is no age limit. |
| `--exclude`, `--no-default-excludes`, `--include` | as for sync | The same [selection](#selection) as sync, with the same built-in patterns. |
| `--force` | off | Delete. Without it, delete only previews. |
| `--dry-run` | off | Only preview, as delete does without `--force`. Refused with `--force`. |

`--trust-state` and `--ignore-state` cannot be combined. All flags are
checked before anything touches the network or the disk, and a bad one exits
2.

Requiring an `--include` with `--ignore-state` makes the selection explicit;
it does not make it narrow. `--include /MyWorks` selects that whole folder.
There is no cap and no list of protected names.

### Proof modes

| Mode | Flags | A file is deleted when |
| --- | --- | --- |
| `hash` | the default | the state records its path with the size and mtime the device lists, and the local copy in `--dest` is a regular file of the recorded size whose SHA-256 is the recorded one. |
| `state` | `--trust-state` | the state records its path with the size and mtime the device lists. The local copy is not read. |
| `none` | `--ignore-state` | it is selected. No state is read. |

In every mode the file must also be old enough for `--older-than`, and
unchanged on the device just before it is removed.

In `hash` mode, a missing `--dest` is a runtime error before delete connects:

```text
destination not found; give --trust-state if the copies were moved or the drive is offline
```

### How delete decides

Delete lists the share as a sync pass does, then goes through the listing in
reverse, so it reaches each folder after everything in it. For each regular
file, the first check that fails decides; the file is then **kept**, which
is an outcome, not a failure:

| # | Check | Modes | If it fails |
| --- | --- | --- | --- |
| 1 | The path is one the share would receive exactly: no backslash, and no empty, `.` or `..` name. | all | WARN `file kept`, `the share would not receive this name exactly` |
| 2 | No other listed path, and in `hash` and `state` modes no recorded path, is the same name in another case or Unicode normalization; see [name clashes](#name-clashes). | all | WARN `file kept`, with the `name clash: ...` reason |
| 3 | The file is at least `--older-than` old. With a positive limit, a file whose mtime is in the future is kept; `--older-than 0` turns the age check off, future mtimes included. | all | counted in `recent`, with no line |
| 4 | The state has a record for exactly this path. | `hash`, `state` | INFO `file kept`, `not in the state` |
| 5 | The device's size and mtime equal the record. | `hash`, `state` | WARN `file kept`, `changed on the device since it was copied` |
| 6 | The local copy exists. | `hash` | INFO `file kept`, `local copy missing; give --trust-state if it was moved` |
| 7 | The local copy is a regular file of the recorded size. | `hash` | WARN `file kept`, `local copy is not a regular file` or `local copy size differs from the state` |
| 8 | The local copy's SHA-256 is the recorded one. | `hash` | WARN `file kept`, `local copy's SHA-256 differs from the state` |
| 9 | Just before removing it, the device still lists a regular file with the same size and mtime. | all | WARN `file kept`, `changed on the device during this run`, or INFO `file kept`, `no longer on the device` |

A local copy that cannot be read (an I/O or permission error) fails that
file with a `file failed` line, and the run goes on. Entries that are neither
regular files nor folders, such as links, are never touched.

A preview stops before check 9 and logs `would delete` for each file that
passes checks 1 to 8. On a share that does not change, a run with `--force`
then deletes exactly those files.

### What the proof does not show

- **The contents on the device.** Equal size and mtime do not prove equal
  contents. A rewrite on the device that keeps both is not noticed. Delete
  does not read device files, which would double the reads.
- **Which telescope this is.** The state names only the destination, not the
  telescope. **Use one destination per telescope.** If one telescope's
  destination is given while delete talks to another, a file at the same
  path with the same size and mtime can be deleted before it was ever copied,
  even in `hash` mode, since the local copy only proves the other telescope's
  file. `--trust-state` cannot catch this at all.
- **Adopted files.** An `adopted` record hashed the local file only; it never
  read the device. Its proof is as good as the copy it adopted.

### Emptied folders

A folder is removed when it held entries before the run and this run removed
every one of them. Anything the selection left out of the listing, and any
file or folder the run kept, keeps its folder. A folder that was already
empty stays, and the share root is never removed. `MyWorks` itself can be
removed when everything under it is deleted; the telescope makes it again.

If something is added to a folder after the listing, the share refuses to
remove it, and delete logs `folder kept` with the reason
`not empty: something was added during the run`.

### The check-then-remove race

Delete checks each file just before it removes it (check 9), in a separate
request. If the telescope rewrites the file in that gap, for example by
restacking, the new bytes are lost. So avoid running delete while the
telescope is busy.

To remove a file or folder that the share marks read-only, delete may first
clear that read-only attribute; if the removal then fails, the attribute can
stay cleared.

### delete and sync

Delete never takes the [history lock](selection-and-history.md#the-history-lock) and never writes
the state file, so it can run while a sync uses the same destination. With
`--force` it takes the [folder lock](selection-and-history.md#the-folder-lock), which a sync holds
only during a pass: it runs between passes, and exits 1 if a pass, another
delete or the app holds it. It reads the state as a sync
[dry run](#dry-run) does: a torn final line is logged and ignored. A file
deleted from the device stays recorded, so the next pass neither copies nor
fails it. A new file the telescope later writes at that path has a new mtime,
and sync replaces the old copy.

- Do not edit or replace files in `--dest` while delete runs. Delete hashes
  the file it opens, and nothing more.
- With `--ignore-state`, delete does not know what sync still needs. Run it
  while sync is not copying, or exclude what sync has not copied yet.

### Stopping a delete

Ctrl+C (SIGINT) or SIGTERM stops delete between files. It logs
`deletion stopped` and exits 0. Nothing is recorded, so there is nothing to
repair; run it again to carry on. A folder that the stopped run emptied is
then already empty, so a later run leaves it. Remove it by hand if you want
it gone.

### delete logs

Delete logs to stderr as sync does. A run with `--force`:

```text
time=2026-11-09T09:00:00.010Z level=INFO msg="deletion starting" proof=hash older_than=7d dry_run=false
time=2026-11-09T09:00:02.410Z level=INFO msg="file deleted" path="MyWorks/M 31/Light_M 31_10.0s_IRCUT_20261029-033119.fit" size=16594560
time=2026-11-09T09:00:02.470Z level=WARN msg="file kept" path="MyWorks/M 31/Stacked_M 31.fit" reason="changed on the device since it was copied"
time=2026-11-09T09:00:02.530Z level=INFO msg="folder removed" path="MyWorks/M 31_sub"
time=2026-11-09T09:00:02.600Z level=INFO msg="deletion done" deleted=212 bytes=3518046720 folders=1 kept=1 recent=40 failed=0 skipped=3 unmatched=0 elapsed=2.59s
```

A preview logs `would delete` (with `path` and `size`) and
`would remove folder` instead, `would skip` for each excluded file or folder,
a `dry run done` summary, and then:

```text
time=2026-11-09T09:00:02.600Z level=INFO msg="preview only; add --force to delete"
```

| Message | Meaning |
| --- | --- |
| `deletion starting` | The proof mode, the age limit and whether this is a preview (`dry_run`). |
| `deleting without the folder lock` | A WARN, with `dest` and `reason`: the destination is on a read-only file system, so the run goes on without the [folder lock](selection-and-history.md#the-folder-lock). |
| `file deleted`, `folder removed` | Removed from the device. |
| `file kept`, `folder kept` | Not removed, with the reason; see [how delete decides](#how-delete-decides). |
| `file failed`, `folder failed` | A failure, with the reason. |
| `skipped` | As in a pass: an excluded file or folder. |
| `deletion done`, `deletion stopped`, `deletion failed` | The summary. A preview's is `dry run done`, `dry run stopped` or `dry run failed`. A failed one carries `source`, `op`, `phase`, `reason` and `error`. |
| `delete failed` | Delete is exiting 1, with `source`, `op=run`, `phase`, `reason` and `error`. |

| Summary key | Meaning |
| --- | --- |
| `deleted` (preview: `delete`) | Files deleted. |
| `bytes` | Their sizes. |
| `folders` (preview: `remove_folders`) | Folders removed. |
| `kept` | Files kept, each with its own `file kept` line. |
| `recent` | Files younger than `--older-than`. |
| `failed` | Files and folders that failed. |
| `skipped`, `unmatched` | As in a pass. |
| `elapsed` | The time the run took. |

### delete exit codes

| Code | Meaning |
| --- | --- |
| 0 | The run or preview finished with no failed file, however many it kept; or a signal stopped it. |
| 1 | The state could not be read; `--dest` is missing in `hash` mode; with `--force`, the [folder lock](selection-and-history.md#the-folder-lock) is held or can't be taken; the connection or listing failed (`connect to ...` or `list the share: ...`); a removal failed on the device, which ends the run (`deletion failed`); or a local copy could not be read (`failed files: N`). A rerun lists the share again and carries on. |
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint`, `--dest` missing without `--ignore-state`, `--trust-state` with `--ignore-state`, `--ignore-state` without `--include` or with `--dest` or `--history-dir`, `--force` with `--dry-run`, a bad `--older-than`, an empty `--history-dir`, a positional argument, a refused `--profile`, or a bad `--scan`. |

> **Warning.** With `--ignore-state`, delete removes whatever is selected,
> whether or not it was copied. With `--older-than 0` as well, that includes
> a file the telescope is still writing. Preview first, without `--force`.

## scan

```sh
seestar-wormhole-cli scan [--format text|json]
```

`scan` lists the Seestars on this network, as the phone app finds them. It
sends the phone app's scan request to UDP port 4720 on the directed
broadcast address of each up IPv4 interface that can broadcast, three times
2 seconds apart, and lists every telescope that answers within 6 seconds.
Loopback and point-to-point (VPN) interfaces are skipped, and so are replies
from this computer's own addresses, such as a proxy here that answers for
the phone app. A reply needs a serial number; replies with the same serial
are one telescope with every address it answered from. scan shows each
telescope's serial number, and never reads the reply's password.

A telescope found here can be given to the other file commands as
`--endpoint ADDRESS:445`. scan connects to nothing and reads no saved
settings: it has no `--endpoint` or `--profile`.

### scan flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--format` | `text` | `text` or `json`. Any other value exits 2 with `rule="use text or json"`. |
| `--log-format` | `text` | As for sync. |
| `--log-level` | `info` | As for sync. |

### scan output

The text format writes one line per address, telescope by telescope in the
order they first answered: the address, `busy` or `-`, the name (the reply's `ssid`), the model and the serial number.
`busy` means the telescope reports as many clients as the phone app allows,
8; the phone app may refuse to connect, but copying still works. The name,
model and serial are quoted as [find's paths](#text-format) are, and an empty
one is `""`, so each line has five fields.

```text
192.0.2.10 - S50_Garden "Seestar S50" 4ab1c2d3
198.51.100.23 busy "Seestar S30" "Seestar S30" 7e8f9a0b
```

The JSON format writes one object on one line, with `telescopes` and
`replies`, the number of replies accepted:

```json
{"telescopes":[{"name":"S50_Garden","model":"Seestar S50","serial":"4ab1c2d3","addresses":["192.0.2.10"],"busy":false}],"replies":3}
```

In both formats, bytes of a name or model that are not UTF-8 become U+FFFD.
A scan keeps at most 64 telescopes and 8 addresses for each; further
replies are only counted.

### scan logs and exit codes

`scan` logs `scanned this network` at INFO with `replies` and `telescopes`,
then `found a telescope` at INFO for each, with `telescope` (its name, model
and serial) and `addresses`, and `no telescope answered` at WARN when the
list is empty. On macOS that warning adds where to allow Local Network
access, which macOS may be blocking.

| Code | Meaning |
| --- | --- |
| 0 | The scan ran, even if nothing answered. |
| 1 | It couldn't scan: no interface to scan from, a send the system refused, or stdout failed. When sends fail with "no route to host" and nothing answers, `scan failed` says `sending was refused (no route to host)`, and on macOS where to allow Local Network access. |
| 2 | A usage error: an unknown or invalid flag, a bad `--format`, or a positional argument. |

## Help, licenses and releases

The file commands share `seestar-wormhole-cli`'s help, `--version`,
`licenses` and flag grammar; see [global command behavior](connection-cli.md#global-command-behavior)
and [licenses](connection-cli.md#licenses). `sync` without `--once` or `--dry-run`,
and `browse`, run until Ctrl+C or SIGTERM stops them; the other file commands
run once and exit. For the app's Copy, Browse and Free up space tabs, which
share the copy history, see [copying images](../ui.md#copying-images). For
checking a download, see [verifying a release](connection-cli.md#verifying-a-release),
and for what a release promises, [versioning](../versioning.md#the-contract).
