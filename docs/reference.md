# Command reference

Use this page when you script `seestar-copy-cli` or need an exact flag, rule or
log line. The [README](../README.md) has a first run.

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
- [licenses](#licenses)
- [Global command behavior](#global-command-behavior)
- [The desktop app](#the-desktop-app): opening it, what it needs, and its
  settings
- [Verifying a release](#verifying-a-release)

## sync

```sh
seestar-copy-cli sync --endpoint HOST:PORT --dest DIR [flags]
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
| `--state-dir` | the per-user config folder's `seestar-copy` | The folder that holds the state file. See [location](#location). It must not be empty when given. |

Durations use Go units, such as `30s`, `5m` or `1h30m`; there is no unit for
days. All flags are checked before anything touches the network or the disk,
and a bad one exits 2.

### Endpoint

The endpoint is either:

- the telescope on your local network, at its IP address and port 445, such
  as `--endpoint 192.0.2.10:445` (use your telescope's address); or
- the SMB listener of
  [seestar-wormhole](https://github.com/zmerlynn/seestar-wormhole), which is
  `127.0.0.1:4445` by default.

Both are handled the same way. Sync logs in as guest with an empty password
and mounts the share `EMMC Images`; neither is configurable. An IPv6 address
goes in brackets, such as `[2001:db8::10]:445`. The port must be 1 to 65535.

Each pass opens its own session and closes it before the wait, so the
telescope's share is free between passes. A failed connection fails that pass
only; see [a failed pass](#a-failed-pass).

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

### Selection

Sync copies the whole share, except files and folders that an exclude pattern
matches; with `--include`, it copies only what an include selects, as
[below](#include). An excluded folder is skipped with everything in it and is never
listed. Excluded files are not copied, adopted or recorded. Files already
recorded in the state stay recorded. Each pass logs a `skipped` line for each
excluded file or folder, with the pattern that matched, and counts them in its
summary; a folder has one line, and what is in it has none. A pass whose
listing fails still logs and counts what it skipped before the failure.

Unless `--no-default-excludes` is given, sync leaves out what these flags
would:

```sh
--exclude '.*'
--exclude '*.tmp'
--exclude 'Thumbs.db'
--exclude 'desktop.ini'
--exclude '$RECYCLE.BIN'
--exclude 'System Volume Information'
--exclude '/log'
--exclude '/tmp_stitch'
--exclude '/batch_stack_tmp'
```

These are dot names such as `.DS_Store`, files the telescope is still writing
such as `*.mp4.tmp`, Windows system files, and the telescope's logs and scratch
folders. They are a fixed list, not a judgment of each file: a folder whose
name starts with a dot is skipped even if it holds images. The public
[Seestar share layout](https://github.com/zmerlynn/seestar-spec/blob/HEAD/spec/media/smb.md#image-store-layout-firmware-975)
for firmware 9.75 describes the root folders such as `log` and `tmp_stitch`,
what writes them, and the `.mp4.tmp` name a video has while it is written.

Each `--exclude` adds a pattern to the built-in ones. To change the built-in
list, give `--no-default-excludes` and repeat the `--exclude` flags you want to
keep. For example, to also leave out videos and the `Preview` folder:

```sh
seestar-copy-cli sync --endpoint 127.0.0.1:4445 --dest ~/Seestar \
  --exclude 'Video' --exclude '/Preview'
```

Quote patterns so that the shell does not expand them. Exclude and include
patterns follow these rules:

- A pattern with no `/` matches a file or folder name at any depth: `Thumbs.db`
  matches `Thumbs.db` and `MyWorks/M 31/Thumbs.db`.
- A pattern with a `/` anywhere is a path from the share root, and must match
  the whole path: `/log` and `log/x` are anchored, `MyWorks/*/Stacked` matches
  `MyWorks/M 31/Stacked` only. One leading `/` is optional.
- `*` matches any run of characters and `?` any one character, both within one
  name; neither matches `/`.
- Case is ignored: both sides are lowercased before they are compared. There is
  no Unicode normalization, so a name spelled with a combining accent does not
  match a pattern spelled with a precomposed one.
- Spaces are part of the pattern.

A pattern that is empty or `/` alone, ends in `/`, contains `[`, `\` or `**`,
or has an empty, `.` or `..` part is a usage error.

#### Include

With one or more `--include` patterns, sync copies only the files that at
least one of them selects, and that no exclude matches. Other files are not
copied, adopted or recorded; files already recorded stay recorded. An
include selects a file if it matches the file's path or the path of any
folder above it:

```sh
--include '/MyWorks'        # everything in the MyWorks folder at the share root
--include '/MyWorks/M 31'   # one target's folder
--include '*.jpg'           # JPEG files in any folder
--include 'Stacked'         # everything in any folder named Stacked
```

Excludes always win, whatever the order of the flags. An include never
brings back a file or folder that an exclude matches, built-in or given:
`--include x --exclude x` selects nothing, and `--include '/log'` selects
nothing unless `--no-default-excludes` is given. This is unlike rsync, where
the first rule that matches decides.

A folder is listed only if an include could select it or something in it.
With anchored patterns such as `/MyWorks`, sync reads just the folders on
the way to them. A pattern without a `/` can match at any depth, so with one
of those every folder that is not excluded is listed. Files and folders that
no include selects have no line of their own; each pass counts them as
`unmatched` in its summary, a folder that is not listed once, without what
is in it, also when the listing then fails. An entry that an exclude matches is logged and counted as
`skipped` instead, whether or not an include would select it.

#### Changing the selection

The first pass after an exclude or an include is removed, or after upgrading
to a release whose selection covers new folders, finds files that were never
recorded.
They are copied or adopted as usual, once they are old enough under the
[readiness](#readiness) rules: with the defaults, files at least 10 minutes old
go in that first pass. Run a [dry run](#dry-run) first to see what that would
be.

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

A dry run creates nothing, not even a missing destination or state folder.
It takes no [lock](#lock), removes no [temporary files](#temporary-files),
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

A failed or stopped download removes its temporary file. At startup, after
taking the [lock](#lock), sync removes every leftover temporary file anywhere
under the destination: only regular files with exactly that form of name, and
never through a symbolic link. It does not resume partial downloads. A folder
under the destination that sync is not allowed to read is skipped with a
warning; any other error reading the destination stops sync with exit 1.

## The state file

The state file is how sync remembers what it has copied. It belongs to sync;
don't edit it while sync is running.

### Location

```text
<state-dir>/sync/<sha256>.jsonl
```

`<sha256>` is the lowercase hex SHA-256 of the absolute, cleaned `--dest`
(a trailing separator is dropped, and a relative path is made absolute). The
path is derived from that string alone: symbolic links are not resolved and
case is not folded. Each destination has its own state file, which is never
kept inside the destination; a `--state-dir` inside the destination is
refused.

The default `--state-dir` is the `seestar-copy` folder in the per-user config
folder:

| Platform | Default state folder |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/seestar-copy`, or `~/.config/seestar-copy` |
| macOS | `~/Library/Application Support/seestar-copy` |
| Windows | `%AppData%\seestar-copy` |

If there is no per-user config folder, sync exits 1 and asks for
`--state-dir`. Keep the state on a local disk: shared and network folders are
not supported.

Sync creates the `sync` folder, and any missing folders above it, with mode
0700 less the umask, so only you can read them. A new state file is set to
exactly 0600, whatever the umask. The lock files are created 0600, less the
umask. Existing folders and lock files keep their permissions. An existing
nonempty state file keeps its permissions; an empty state file is replaced
with a new 0600 file. On Windows the mode bits do not apply, and privacy
comes from the ACLs the folder inherits.

The state is keyed by destination only. With a second telescope, use a second
destination.

### One spelling per destination

Always give the same destination the same way, such as `--dest ~/Seestar`
every time. Another spelling of the same folder, such as through a symbolic
link, or in another case on a case-insensitive disk, gives a different state
file. At startup sync compares the destination with those named by the other
state files in the same `sync` folder, and refuses to start if one is the same
folder under another spelling:

```text
destination /mnt/b is the same folder as /mnt/a, which already has the state file .../sync/<sha256>.jsonl; use one spelling per destination
```

This check is best effort. It cannot compare a destination that is gone or
unreadable, and it does not look in another `--state-dir`.

### Format

JSON Lines, append-only. The first line is a header with the format version
and the absolute destination:

```json
{"v":1,"dest":"/home/user/Seestar"}
```

Every later line records one file that is complete in the destination:

```json
{"path":"MyWorks/M 31/light_001.fit","size":16000000,"mtime":"2026-10-29T03:31:19Z","sha256":"<64 hex digits>","at":"2026-11-02T01:02:03.456789Z","how":"copied"}
```

| Member | Meaning |
| --- | --- |
| `path` | The file's path on the share, with forward slashes. |
| `size` | Its size in bytes, as listed by the device. |
| `mtime` | The device's mtime, in RFC 3339 UTC. |
| `sha256` | The lowercase hex SHA-256 of the local file's contents. |
| `at` | When the line was written, in RFC 3339 UTC. |
| `how` | `copied`, `replaced` or `adopted`; see [how a pass decides](#how-a-pass-decides). |

When a path has several lines, the last one counts. A line is written and
fsynced only after its file is complete under its final name.

Reading is permissive: unknown members are ignored, and times may carry any
RFC 3339 offset. Some damage is repaired, and the rest stops sync:

- A final line without a newline, or one that is not a valid record, is what
  a crash leaves. It is dropped with a warning, and the file is cut back to
  the last good line. A [dry run](#dry-run) ignores it and leaves the file
  as it is.
- Any other bad line, a line longer than 1 MiB, a header with another version,
  or a header that names another destination stops sync with exit 1. The file
  is left unchanged.

Deleting a destination's state file (while sync is not running for it) makes
sync forget that destination. The next pass adopts the files still in the
destination and copies again everything that is not, including files you had
moved away.

### Lock

Only one sync at a time may use a destination. Sync holds an exclusive OS
lock on `<sha256>.lock`, next to the state file, while it runs. A second sync
for the same destination, spelled the same way and with the same
`--state-dir`, exits 1:

```text
destination /home/user/Seestar: another sync is using this destination (lock file .../sync/<sha256>.lock)
```

The lock is released when the process exits, however it exits. The lock files,
and `registry.lock` in the same folder, stay on disk; that is expected.

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

Sync logs to stderr, as text lines with UTC timestamps to the millisecond.
Help, `--version` and `licenses` write to stdout. Per-file lines are logged as
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
the [versioning contract](versioning.md#not-in-the-contract); a release that
changes them says so in its notes.

| Level | Message | Keys | When |
| --- | --- | --- | --- |
| INFO | `file copied` | `path`, `size`, `overwrote=true` when it replaced a different local file, and `elapsed`, the file's download time | A file was downloaded and recorded. |
| INFO | `file replaced` | `path`, `size`, `elapsed` | A file that changed on the device was downloaded again. |
| INFO | `file adopted` | `path`, `size` | A file already in the destination was hashed and recorded. |
| WARN | `file failed` | `path`, `reason` | A file failed this pass; it is tried again next pass. |
| INFO | `skipped` | `path`, `exclude` | The file or folder matches the pattern `exclude`, the first one that matched, and is left out, by a pass or by `find`. A folder has one line; what is in it is not listed. See [selection](#selection). |
| INFO | `pass done` | see below | A pass finished. |
| ERROR | `pass failed` | see below, and `error` | The connection or a listing failed. |
| INFO | `pass stopped` | see below | A signal stopped the pass. |
| INFO | `would copy` | `path`, `size`, and `overwrite=true` when it would replace a different local file | Dry run: the file would be downloaded and recorded as `copied`. |
| INFO | `would replace` | `path`, `size` | Dry run: the file changed on the device and would be downloaded again. |
| INFO | `would adopt` | `path`, `size` | Dry run: the file is already in the destination and would be hashed and recorded. |
| INFO | `would wait` | `path`, `size` | Dry run: the file is not ready yet. |
| INFO | `would skip` | `path`, `exclude` | Dry run: the same as `skipped`. |
| INFO | `no copied files recorded under this top-level folder` | `folder` | Dry run: the state has no file under this folder at the share root, such as a folder a new release now covers. |
| INFO | `dry run done` | `copy`, `replace`, `adopt`, `waiting`, `failed`, `skipped`, `unmatched`, `bytes`, `elapsed` | A dry run finished. Counts other than `unmatched` are of the lines above and of `file failed` lines. `unmatched` counts entries left out by includes, without a line of their own; `bytes` is what it would download. |
| ERROR | `dry run failed` | as `dry run done`, and `error` | The connection or a listing failed in a dry run. |
| INFO | `dry run stopped` | as `dry run done` | A signal stopped a dry run. |
| WARN | `dropped the torn final line of the state file` | `file`, `line`, `reason` | See [format](#format). |
| WARN | `ignored the torn final line of the state file` | `file`, `line`, `reason` | The same, in a [dry run](#dry-run), which leaves the file as it is. |
| WARN | `skipped a folder that cannot be read while removing leftover temporary files` | `folder`, `reason` | See [temporary files](#temporary-files). |
| WARN | `skipped a malformed state file while checking for another spelling of the destination` | `file`, `reason` | Another state file in the `sync` folder cannot be read. |
| ERROR | `sync failed` | `error` | Sync is exiting 1. |
| INFO | `listing done` | see [find logs](#find-logs) | A `find` listing finished. |
| ERROR | `listing failed` | as `listing done`, and `error` | The connection or a listing failed in `find`. |
| ERROR | `find failed` | `error` | Find is exiting 1. |
| ERROR | `invalid arguments` | `option`, `rule`, `help` | A usage error; the command is exiting 2. |

Every pass ends with one summary line, whose keys are:

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
flag and the rule it broke, and the help command to run:

```text
time=2026-11-02T03:00:00.000Z level=ERROR msg="invalid arguments" option=--workers rule="must be 1 to 64" help="seestar-copy-cli sync --help"
```

Flag values are never echoed in it.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Stopped by a signal; a `--once` or `--dry-run` pass with no failed files; or help, `--version` or `licenses` printed. |
| 1 | A runtime failure: the destination or state could not be opened or locked, leftover temporary files could not be removed, a `--once` or `--dry-run` pass failed (`connect to ...` or `list the share: ...`), a `--once` or `--dry-run` pass had failed files (`failed files: N`), or output could not be written. |
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint` or `--dest`, `--dry-run` with `--interval`, or a positional argument. |

Without `--once` or `--dry-run`, failed passes and failed files do not end
sync: it logs them and tries again on the next pass.

For `find`, see [find exit codes](#find-exit-codes); for `delete`, see
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
  [Deleting the state file](#format) makes sync copy every missing file again.
- **Rename over an open file.** Replacing or overwriting a local file fails
  if another program, such as an image viewer, has it open without allowing
  deletion. That file fails for the pass and is tried again on the next.
- **Names.** See [on Windows](#on-windows).

## find

```sh
seestar-copy-cli find --endpoint HOST:PORT [flags]
```

Lists the files and folders on the telescope's share, once, with one line per
entry on stdout. It selects what a sync pass would list, and with `--dest` it
says for each file whether that destination's [state](#the-state-file) has
it. Like sync, it only reads from the telescope. It writes nothing on disk:
not the destination, not the state, not a lock.

```sh
seestar-copy-cli find --endpoint 127.0.0.1:4445 --include /MyWorks
seestar-copy-cli find --endpoint 127.0.0.1:4445 --dest ~/Seestar --older-than 7d
seestar-copy-cli find --endpoint 127.0.0.1:4445 --format jsonl > share.jsonl
```

### find flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | none; required | As for sync. See [endpoint](#endpoint). |
| `--exclude`, `--no-default-excludes`, `--include` | as for sync | The same [selection](#selection) as sync, with the same built-in patterns. |
| `--older-than` | no limit | List only files whose device mtime is at least this old. See below. |
| `--format` | `text` | `text` or `jsonl`. See [text format](#text-format) and [JSONL format](#jsonl-format). |
| `--dest` | none | A destination whose state gives each file a [status](#statuses). It is never created, and need not exist. It must not be empty when given. |
| `--state-dir` | as for sync | The folder that holds the state file of `--dest`; see [location](#location). Only with `--dest`, and it must not be empty when given. |

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
$ seestar-copy-cli find --endpoint 127.0.0.1:4445 --include /MyWorks --dest ~/Seestar
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

Find takes no [lock](#lock), so it may run while a sync uses the same
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
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint`, a bad `--format` or `--older-than`, an empty `--dest` or `--state-dir`, `--state-dir` without `--dest`, or a positional argument. |

## browse

```sh
seestar-copy-cli browse --endpoint HOST:PORT [--dest DIR] [--listen ADDR] [--open]
```

Serves a read-only web page for looking through the telescope's share. The
page lists one folder at a time, shows a `.jpg`, a `.txt` or a FITS header
when you click it, and saves any file to this computer through the browser. With `--dest`, each file
shows its [status](#statuses) in that destination's state, as for find.
Like sync and find, browse only reads from the telescope, and it writes
nothing on disk: not the destination, not the state, not a lock.

```sh
seestar-copy-cli browse --endpoint 127.0.0.1:4445 --open
seestar-copy-cli browse --endpoint 127.0.0.1:4445 --dest ~/Seestar
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
| `--state-dir` | as for sync | As for find: the folder that holds the state file of `--dest`. Only with `--dest`, and it must not be empty when given. |

Browse has no selection: it shows the whole share, and `--exclude`,
`--no-default-excludes` and `--include` are unknown flags.

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
are left out, as find leaves them out. Click a folder to open it; the
browser's Back button returns to the previous folder.

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
has changed, so the statuses follow a sync running at the same time. Browse takes no [lock](#lock). A file still being written can be shown
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
| WARN | `connect failed` | `endpoint`, `error` | The login failed or took longer than 30 seconds. The requests waiting on it are answered with the error. |
| WARN | `request failed` | `path`, `error` | A listing or file read failed on the device, the state could not be read, or a download broke after it started. Not for a refused request, a path that does not exist, or a browser that went away. |
| WARN | `could not open a browser` | `error` | `--open` failed. Browse keeps serving; open the printed URL yourself. |
| WARN | `ignored the torn final line of the state file` | `file`, `line`, `reason` | As in find. |
| INFO | `browse stopped` | | A signal stopped browse. |
| ERROR | `browse failed` | `error` | Browse is exiting 1. |

### browse exit codes

| Code | Meaning |
| --- | --- |
| 0 | Stopped by a signal. |
| 1 | Browse could not start or keep serving: the address could not be listened on, the state of `--dest` could not be read when browse started, the URL could not be written to stdout, or the HTTP server stopped with an error. |
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint`, a bad `--listen`, an empty `--dest` or `--state-dir`, `--state-dir` without `--dest`, or a positional argument. |

## delete

```sh
seestar-copy-cli delete --endpoint HOST:PORT --dest DIR [--force] [flags]
seestar-copy-cli delete --endpoint HOST:PORT --dest DIR --trust-state [--force] [flags]
seestar-copy-cli delete --endpoint HOST:PORT --ignore-state --include PAT [--force] [flags]
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
seestar-copy-cli delete --endpoint 127.0.0.1:4445 --dest ~/Seestar
seestar-copy-cli delete --endpoint 127.0.0.1:4445 --dest ~/Seestar --force
seestar-copy-cli delete --endpoint 127.0.0.1:4445 --ignore-state --include '*.jpg' --older-than 30d --force
```

### delete flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | none; required | As for sync. See [endpoint](#endpoint). |
| `--dest` | none; required, except with `--ignore-state`, which refuses it | The destination whose [state](#the-state-file), and by default whose local copies, prove that a file was copied. It is never created. It must be the destination of **this** telescope; see [what the proof does not show](#what-the-proof-does-not-show). |
| `--state-dir` | as for sync | The folder that holds the state file of `--dest`; see [location](#location). Refused with `--ignore-state`, and it must not be empty when given. |
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

Delete takes no [lock](#lock) and never writes the state file, so it can run
while a sync uses the same destination. It reads the state as a sync
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
| `file deleted`, `folder removed` | Removed from the device. |
| `file kept`, `folder kept` | Not removed, with the reason; see [how delete decides](#how-delete-decides). |
| `file failed`, `folder failed` | A failure, with the reason. |
| `skipped` | As in a pass: an excluded file or folder. |
| `deletion done`, `deletion stopped`, `deletion failed` | The summary. A preview's is `dry run done`, `dry run stopped` or `dry run failed`. |

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
| 1 | The state could not be read; `--dest` is missing in `hash` mode; the connection or listing failed (`connect to ...` or `list the share: ...`); a removal failed on the device, which ends the run (`deletion failed`); or a local copy could not be read (`failed files: N`). A rerun lists the share again and carries on. |
| 2 | A usage error: an unknown or invalid flag, a missing `--endpoint`, `--dest` missing without `--ignore-state`, `--trust-state` with `--ignore-state`, `--ignore-state` without `--include` or with `--dest` or `--state-dir`, `--force` with `--dry-run`, a bad `--older-than`, an empty `--state-dir`, or a positional argument. |

> **Warning.** With `--ignore-state`, delete removes whatever is selected,
> whether or not it was copied. With `--older-than 0` as well, that includes
> a file the telescope is still writing. Preview first, without `--force`.

## licenses

```sh
seestar-copy-cli licenses
```

Prints the project's NOTICE and LICENSE, then, for each third-party module
linked into this binary, a `Module:` line with its path, an `SPDX:` line with
its license identifier, and the full texts of its license and notice files.
It needs no network and no source checkout. It takes no flags except
`--help`/`-h` and no arguments; anything else exits 2, and a failure to write
the output exits 1. See [NOTICE](../NOTICE).

## Global command behavior

- `seestar-copy-cli` with no arguments, `--help` or `-h` prints help and
  exits 0. `seestar-copy-cli help sync` and `seestar-copy-cli sync --help`
  print a command's help.
- `seestar-copy-cli --version` prints
  `seestar-copy-cli <version> (<revision>, <os>/<arch>)`, such as
  `seestar-copy-cli development (development, linux/arm64)` for a build
  without release metadata. `--version` belongs to the top-level command
  only.
- Flags take their value as `--flag value` or `--flag=value`. Boolean flags
  need no value; `--once=false` turns one off. A flag given twice keeps the
  last value, except `--exclude`, which collects every value.
- Help and version need no network and touch no files.
- `sync` without `--once` or `--dry-run`, and `browse`, run until Ctrl+C or
  SIGTERM stops them. The other commands run once and exit.

## The desktop app

Every download also holds the desktop app, `seestar-copy`. It copies, browses
and frees up space in one window, with the same rules and state files as
`sync`, `browse` and `delete`.

- **macOS:** open the DMG and drag `seestar-copy.app` to Applications, then
  open it from there.
- **Windows:** unpack the archive and open `seestar-copy.exe`. It needs the
  Microsoft Edge WebView2 Runtime, which Windows 10 and 11 normally include;
  if it is missing or broken, the app says so. The app is not code-signed, so
  Windows may say "Windows protected your PC": choose **More info**, then
  **Run anyway**.
- **Linux:** unpack the archive and run `./seestar-copy`. It needs GTK 4,
  WebKitGTK 6.0 and bubblewrap (on Ubuntu 24.04: `libgtk-4-1`,
  `libwebkitgtk-6.0-4` and `bubblewrap`), and WebKit's sandbox needs
  unprivileged user namespaces. Without them the app says what is missing
  and exits. `seestar-copy-cli` needs none of these.

The app keeps its settings in `app.json` in the default state folder (see
[location](#location)), or in the file `--settings FILE` names. Each settings
file is one window; run the app again with another `--settings` file for
another setup. `seestar-copy-cli` never reads `app.json`. While copying is on,
the app holds the folder's state lock, as `sync` does, so a `sync` for the
same folder exits with its usual error, and the app shows "Folder in use"
while a `sync` holds it.

The first time the app opens on a settings file, it opens on the Settings
tab, and the Copy, Browse and Free up space tabs stay unavailable until the
telescope address and the folder are saved. After that first **Save**, the
app shows the Copy tab with copying off; **Start copying** begins. **Start
copying when the app opens** is off for a new settings file. A settings file
that already exists keeps its value, which is on if the file does not have the
setting.

Settings shows, under the folder, **Copy history**: the path of the folder's
state file (see [the state file](#the-state-file)), which `sync` uses too. It
shows the folder as saved; while the Folder field holds a folder not yet
saved, it says to save first. **Clear history…** deletes that state file
after a confirmation, so seestar-copy forgets what it copied to the folder.
Files still in the folder are kept and adopted on the next pass; files moved
out of it are copied again. Clear history is unavailable while copying is on,
and it refuses while Free up space is checking or deleting, or while a `sync`
holds the folder's lock. It also discards a Free up space preview.

The bottom of Settings shows the settings file in use. To start over with a
new setup, quit the app and delete that file, or open the app with
`--settings` and a new file.

## Verifying a release

Each release lists a SHA-256 checksum for its four archives and universal Mac
DMG in the single `SHA256SUMS`, and carries a GitHub build provenance
attestation for each of those five downloads. With the downloaded assets and
`SHA256SUMS` in one folder:

```sh
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify <downloaded-asset> --repo zmerlynn/seestar-copy
```

Linux and Windows CLI executables are byte-for-byte reproducible with
identical source, version, revision and Go toolchain: rebuild and compare
against `seestar-copy-cli` (with `.exe` on Windows) unpacked from the archive.
See [cutting a release](development/releasing.md). The Mac CLI is combined
with lipo and signed, so it is not byte-reproducible. The desktop app has no
byte-reproducibility promise. The app executables and the DMG are verified by
`SHA256SUMS` and attestation only. See [versioning](versioning.md) for what a
release promises.
