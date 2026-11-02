# Command reference

Use this page when you script `seestar-copy-cli` or need an exact flag, rule or
log line. The [README](../README.md) has a first run.

- [sync](#sync): flags, endpoint, destination layout, [selection](#selection) and
  [dry run](#dry-run)
- [How a pass decides](#how-a-pass-decides)
- [Copying a file](#copying-a-file) and [temporary files](#temporary-files)
- [The state file](#the-state-file)
- [Stopping](#stopping)
- [Logs](#logs) and [exit codes](#exit-codes)
- [Names that fail](#names-that-fail)
- [Windows](#windows)
- [licenses](#licenses)
- [Global command behavior](#global-command-behavior)
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
not supported. Sync creates the `sync` folder readable by you only (0700 on
Linux and macOS) and its files 0600.

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

Sync logs to stderr, as text lines in Go's `log/slog` format, with UTC
timestamps to the millisecond. Help, `--version` and `licenses` write to
stdout. Per-file lines are logged as each file finishes. A pass that skips the
`log` folder and copies two files looks like this:

```text
time=2026-11-02T03:10:00.118Z level=INFO msg=skipped path=log exclude=/log
time=2026-11-02T03:10:01.620Z level=INFO msg="file copied" path="MyWorks/M 31/light_002.fit" size=16000000
time=2026-11-02T03:10:03.290Z level=INFO msg="file copied" path="MyWorks/M 31/light_003.fit" size=16000000
time=2026-11-02T03:10:03.502Z level=INFO msg="pass done" copied=2 replaced=0 adopted=0 waiting=1 failed=0 skipped=1 unmatched=0 bytes=32000000 elapsed=3.502s
```

Values with spaces or quotes are quoted. The messages and keys below are
what this release logs. They are not part of the
[versioning contract](versioning.md#not-in-the-contract); a release that
changes them says so in its notes.

| Level | Message | Keys | When |
| --- | --- | --- | --- |
| INFO | `file copied` | `path`, `size`, and `overwrote=true` when it replaced a different local file | A file was downloaded and recorded. |
| INFO | `file replaced` | `path`, `size` | A file that changed on the device was downloaded again. |
| INFO | `file adopted` | `path`, `size` | A file already in the destination was hashed and recorded. |
| WARN | `file failed` | `path`, `reason` | A file failed this pass; it is tried again next pass. |
| INFO | `skipped` | `path`, `exclude` | The file or folder matches the pattern `exclude`, the first one that matched, and is left out. A folder has one line; what is in it is not listed. See [selection](#selection). |
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
| ERROR | `invalid arguments` | `option`, `rule`, `help` | A usage error; sync is exiting 2. |

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

## Verifying a release

Each release lists a SHA-256 checksum for its four archives and universal Mac
DMG in the single `SHA256SUMS`, and carries a GitHub build provenance
attestation for each of those five downloads. With the downloaded assets and
`SHA256SUMS` in one folder:

```sh
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify <downloaded-asset> --repo zmerlynn/seestar-copy
```

Linux and Windows executables are byte-for-byte reproducible with identical
source, version, revision and Go toolchain: rebuild and compare against
`seestar-copy-cli` (with `.exe` on Windows) unpacked from the archive. See
[cutting a release](development/releasing.md). The Mac executable is combined
with lipo and signed, so it is not byte-reproducible; the DMG is verified by
`SHA256SUMS` and attestation only. See [versioning](versioning.md) for what a
release promises.
