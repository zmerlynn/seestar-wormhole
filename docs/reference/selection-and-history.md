# Selection and copy history

The app's **Copy**, **Browse** and **Free up space** tabs and
`seestar-wormhole-cli`'s `sync`, `find`, `browse` and `delete` choose files
the same way and remember copies in the same copy history. This page has
those shared rules: which files are selected, where the copy history is kept,
and the locks that keep a copy and a delete apart. The tabs are in
[Using the UI](../ui.md#copying-images) and the commands in the
[file command reference](files-cli.md).

- [Selection](#selection): the [built-in patterns](#built-in-patterns),
  [pattern rules](#pattern-rules), [include](#include) and
  [changing the selection](#changing-the-selection)
- [Copy history](#copy-history): its [location](#location),
  [one spelling per destination](#one-spelling-per-destination) and
  [format](#format)
- [Locks](#locks): [the history lock](#the-history-lock),
  [the folder lock](#the-folder-lock) and
  [when the other side holds a lock](#when-the-other-side-holds-a-lock)

## Selection

Each place that picks files takes exclude patterns, include patterns and the
built-in patterns:

| Where | Exclude | Include | Built-in patterns |
| --- | --- | --- | --- |
| The app's Copy, and Browse's statuses | **Leave out** | **Only copy** | On while **Skip the telescope's logs, scratch and system files** is on |
| The app's Free up space | **Keep on the telescope** | **Only free up** | Always on |
| `sync`, `find`, `browse` | `--exclude` | `--include` | On unless `--no-default-excludes` is given |
| `delete` | `--exclude` | `--include` | On unless `--no-default-excludes` is given |

Free up space's patterns are separate from Copy's: **Leave out** and **Only
copy** don't change what is deleted.

A file is selected when no exclude matches it or a folder above it, and, when
there are includes, at least one include selects it. An excluded folder is
skipped with everything in it and is never listed. Excluded files are not
copied, adopted or recorded. Files already recorded in the copy history stay
recorded.

### Built-in patterns

The built-in patterns leave out what these flags would:

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
seestar-wormhole-cli sync --endpoint 127.0.0.1:4445 --dest ~/Seestar \
  --exclude 'Video' --exclude '/Preview'
```

### Pattern rules

Quote patterns on the command line so that the shell does not expand them.
In the app, put one pattern on each line. Exclude and include patterns use
[.gitignore syntax](https://git-scm.com/docs/gitignore#_pattern_format),
except:

- Case is ignored: both sides are lowercased before they are compared. There is
  no Unicode normalization, so a name spelled with a combining accent does not
  match a pattern spelled with a precomposed one. Since patterns are
  lowercased, `[[:upper:]]` matches nothing.
- A leading `!` is a usage error. Excludes always win, so give the files to
  keep as an include instead. To match a name that starts with `!`, write `\!`.
- An include selects everything in a folder that it matches. See
  [Include](#include).
- A `\` before a `/` is a usage error, though git reads `\/` as a plain `/`:
  write a plain `/`. A class that holds a `/`, such as `[a/]`, is a usage
  error too.

In short:

- A pattern with no `/`, or only a trailing one, matches a file or folder name
  at any depth: `Thumbs.db` matches `Thumbs.db` and `MyWorks/M 31/Thumbs.db`.
- A pattern with a `/` at the start or in the middle is a path from the share
  root, and must match the whole path: `/log` and `log/x` are anchored,
  `MyWorks/*/Stacked` matches `MyWorks/M 31/Stacked` only.
- A trailing `/` matches folders only: `Stacked/` matches a folder named
  `Stacked`, not a file.
- `*` matches any run of characters, `?` any one byte and `[...]` one byte
  from a class such as `[0-9]` or `[!a]`, all within one name; none of them
  matches `/`. A letter outside ASCII is more than one byte: `星` is three.
  Before v0.7.0, `?` matched one letter, so a pattern written for that now
  selects differently, both ways. An exclude `M?` no longer matches
  `MyWorks/M星`, so it no longer keeps that folder from being copied or
  deleted. An include `M???` now matches `MyWorks/M星`, so it now selects that
  folder. After upgrading, run the preview again before deleting: `delete`
  without `--force`, or Preview in Free up space.
- `**` as a whole name matches any number of folders: `**/Stacked` matches
  `Stacked` at any depth, `MyWorks/**/*.fit` matches FITS files anywhere under
  `MyWorks`, and `MyWorks/**` everything in `MyWorks`.
- `\` makes the next character literal: `\*`, `\[`, `\#` or `\!`. It can't
  escape a `/`.
- Spaces at the start or in the middle of a pattern are part of it: `M 31`.

A pattern is a usage error, rather than one that silently matches nothing,
if it:

- is empty or `/` alone, or has an empty, `.` or `..` part, such as `a//b`;
- starts with `!` or `#`, which in a `.gitignore` file mean a negation and a
  comment;
- ends in an unescaped space or backslash: write `\ ` for a literal trailing
  space and `\\` for a literal backslash;
- has an unescaped `\` before a `/`, such as `MyWorks\/M31`: write a plain
  `/`. In `a\\/b` the backslash is escaped, so the `/` separates names and the
  pattern is valid;
- holds a line break;
- has a `[` with no closing `]`, or names a class other than the POSIX ones
  such as `[:digit:]`. A class can't hold a `/`: `[a/]` is a usage error.

### Include

With one or more includes, only the files that at least one of them selects,
and that no exclude matches, are selected. Other files are not copied,
adopted or recorded; files already recorded stay recorded. An include selects
a file if it matches the file's path or the path of any folder above it:

```sh
--include '/MyWorks'        # everything in the MyWorks folder at the share root
--include '/MyWorks/M 31'   # one target's folder
--include '*.jpg'           # JPEG files in any folder
--include 'Stacked'         # everything in any folder named Stacked
--include 'MyWorks/**/*.fit' # FITS files anywhere in MyWorks
```

To copy FITS files only, as the app's **FITS files only** does, and leave the
JPG previews and other files:

```sh
seestar-wormhole-cli sync --endpoint 127.0.0.1:4445 --dest ~/Seestar \
  --include '*.fit' --include '*.fits'
```

Excludes always win, whatever the order of the flags. An include never
brings back a file or folder that an exclude matches, built-in or given:
`--include x --exclude x` selects nothing, and `--include '/log'` selects
nothing unless `--no-default-excludes` is given. This is unlike rsync, where
the first rule that matches decides.

A folder is listed only if an include could select it or something in it.
With anchored patterns such as `/MyWorks`, just the folders on the way to
them are read. With `/MyWorks/**/*.fit`, every folder in `MyWorks` is read,
since `**` can stand for any of them. A pattern that can match at any depth,
such as `*.jpg` or `**/Stacked`, could match in any folder, so with one of
those every folder that is not excluded is listed.

### Changing the selection

The first pass after an exclude or an include is removed, or after upgrading
to a release whose selection covers new folders, finds files that were never
recorded.
They are copied or adopted as usual, once they are old enough under the
[readiness](files-cli.md#readiness) rules: with the defaults, files at least 10 minutes old
go in that first pass. Run a [dry run](files-cli.md#dry-run), or the app's
**Preview**, first to see what that would be.

## Copy history

The copy history is how the app's Copy and `sync` remember what they have
copied: one state file per destination folder. Free up space and `delete`
read it to prove that a file is safely copied, and Browse and `find` read it
for their statuses. It belongs to the copy that writes it; don't edit it
while copying runs.

### Location

```text
<history-dir>/sync/<sha256>.jsonl
```

`<sha256>` is the lowercase hex SHA-256 of the absolute, cleaned `--dest`
(a trailing separator is dropped, and a relative path is made absolute). The
path is derived from that string alone: symbolic links are not resolved and
case is not folded. Each destination has its own state file, which is never
kept inside the destination; a `--history-dir` inside the destination is
refused.

The default `--history-dir` is the `seestar-copy` folder in the per-user config
folder, the one earlier releases' `seestar-copy-cli` used, so an upgrade keeps
its history:

| Platform | Default state folder |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/seestar-copy`, or `~/.config/seestar-copy` |
| macOS | `~/Library/Application Support/seestar-copy` |
| Windows | `%AppData%\seestar-copy` |

The app's Copy uses the same default, whatever the app's `--state-dir` says,
so the app and `sync` share one history for a folder. The app's **Copy
history folder**, saved in `copy.json` as `history_dir`, puts the app's
elsewhere; see [Copy](../ui.md#copy). To share it with a `sync`, give the
same folder as `--history-dir`.

If there is no per-user config folder, sync exits 1 and asks for
`--history-dir`. Keep the state on a local disk: shared and network folders are
not supported.

Sync creates the `sync` folder, and any missing folders above it, with mode
0700 less the umask, so only you can read them. A new state file is set to
exactly 0600, whatever the umask. The lock files are created 0600, less the
umask. Existing folders and lock files keep their permissions. An existing
nonempty state file keeps its permissions; an empty state file is replaced
with a new 0600 file. On Windows the mode bits do not apply, and privacy
comes from the ACLs the folder inherits.

The state is keyed by destination only. The app gives each telescope its own
folder, and so its own history; see
[more than one telescope](../copying.md#more-than-one-telescope). With
`sync`, give a second telescope a second destination, or use
`--profile default`, which fills the saved telescope's folder.

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
unreadable, and it does not look in another `--history-dir`.

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
| `how` | `copied`, `replaced` or `adopted`; see [how a pass decides](files-cli.md#how-a-pass-decides). |

When a path has several lines, the last one counts. A line is written and
fsynced only after its file is complete under its final name.

Reading is permissive: unknown members are ignored, and times may carry any
RFC 3339 offset. Some damage is repaired, and the rest stops sync:

- A final line without a newline, or one that is not a valid record, is what
  a crash leaves. It is dropped with a warning, and the file is cut back to
  the last good line. A [dry run](files-cli.md#dry-run) ignores it and leaves the file
  as it is.
- Any other bad line, a line longer than 1 MiB, a header with another version,
  or a header that names another destination stops sync with exit 1. The file
  is left unchanged.

Deleting a destination's state file (while sync is not running for it) makes
sync forget that destination. The next pass adopts the files still in the
destination and copies again everything that is not, including files you had
moved away. The app's **Clear history…** does this for Copy's saved folder.

## Locks

Sync takes two exclusive OS locks: the history lock for the whole run, and
the folder lock during each pass. A lock is released when its process
exits, however it exits. The lock files stay on disk; that is expected.

### The history lock

Only one sync at a time may use a state file. Sync holds a lock on
`<sha256>.lock`, next to the state file, while it runs. A second sync for the
same destination, spelled the same way and with the same `--history-dir`,
exits 1:

```text
destination /home/user/Seestar: another sync is using this destination (lock file .../sync/<sha256>.lock)
```

While copying is on, the app holds the same lock for its folder, as `sync`
does. `registry.lock`, in the same folder, stays on disk too.

### The folder lock

Every sync pass, and every `delete --force` that has a destination, takes a
lock on `.seestar-wormhole.lock` at the top of the destination, whatever
its `--history-dir`. A pass holds it from before it removes leftover
[temporary files](files-cli.md#temporary-files) until its downloads have ended and their
temporary files are removed, and releases it before waiting for the next
pass, so a delete can run between passes. A delete holds it from before it
lists the share until its last removal. The app's Copy and Free up space take
it as sync and delete do, and the app's Browse takes it while it saves a
file into the folder. The file is created empty with mode 0600, less the
umask, and is never removed; leave it in the folder.

```text
destination /home/user/Seestar: another copy or delete is using this folder (lock file /home/user/Seestar/.seestar-wormhole.lock)
```

The lock is in the folder, so every way of naming the folder reaches the same
lock: a relative path, a symbolic link, a bind mount, another letter case on
a case-insensitive volume, or a new name after the folder is renamed. It does
not cover a destination inside another one (`A` and `A/sub`), a folder on a
network file system whose locks don't reach other machines, a read-only view
of a folder that is written through another path, or `seestar-copy-cli` and
older releases of seestar-wormhole, which don't take it. Stop those before
upgrading.

A dry run, a delete preview, `delete --ignore-state` (which has no
destination), `find` and `browse` take no folder lock. Nor does a delete
whose destination is missing: with `--trust-state` it goes on, since it
doesn't read the folder, and in `hash` mode it stops at its
missing-destination error. On a read-only file system, where the lock file
can't be created, sync exits 1, and delete goes on without the lock, with a
`deleting without the folder lock` warning. Such a delete isn't kept apart
from a sync that writes the same folder through a writable path, so its
hash proof may read a file that sync is replacing. Any other error creating
or locking the file, such as permission denied or a file system without
locks, stops sync and delete with exit 1, naming the lock file, before
anything is listed.

### When the other side holds a lock

What each one does when another sync, a `delete --force` or the app holds a
lock it needs:

| Who | What happens |
| --- | --- |
| `sync`, at its first pass | Exits 1 with the lock error above. |
| `sync`, at a later pass | Skips the pass: it logs `pass skipped` with `reason=folder_busy`, connects to nothing, and tries again after `--interval`. |
| `delete --force` | Exits 1 before it lists the share. |
| The app's Copy, when you start copying | Says **Another copy or delete is using this folder.** Copying stays off. |
| The app's Copy, at a later check | Skips that check, copies nothing, says **Another copy or delete is using this folder.**, and tries again at the next check. |
| The app's Free up space | Stops at once with **Another copy or delete is using this folder.** and deletes nothing. |
| The app's Browse, saving into the folder | Says **Another copy or delete is using this folder. Try saving it again.** and saves nothing. |

So the app's Copy and a `sync` never copy into one folder at once. Free up
space and `delete --force` run between their checks and passes.
