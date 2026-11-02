# Versioning and compatibility

## Principle

seestar-copy follows Semantic Versioning 2.0.0 over a declared **contract**. The version number promises only what
the contract lists. Anything not listed can change in any release.

The telescope's firmware and its SMB share change without notice and are not covered by the number. Each release
instead carries a **compatibility record**: what was actually tested with that artifact, and what is carried forward
from earlier evidence.

## The release unit

A release is four archives (linux and windows x amd64, arm64), one universal macOS DMG, and `SHA256SUMS`, built by
the release workflow from an annotated tag.

## The contract

The [command reference](reference.md) has the detail of each item.

1. **Commands and flags.** `sync`, `find`, `delete`, `licenses`, `help` and the top-level `--version`; flag names,
   meanings and grammar; the defaults of every `sync`, `find` and `delete` flag, as in [flags](reference.md#flags),
   [find flags](reference.md#find-flags) and [delete flags](reference.md#delete-flags), including delete's
   `--older-than` default of `7d`; and the rules about which values are refused. `--older-than` takes a Go
   duration or a whole number of days such as `7d`, not both, for find and delete alike. For delete:
   `--trust-state` and `--ignore-state` cannot be combined; `--ignore-state` needs at least one `--include` and
   refuses `--dest` and `--state-dir`; without `--force`, delete only previews, as `--dry-run` does, and `--force`
   with `--dry-run` is refused. `--force` only means "act": it never skips or weakens the proof, and
   `--trust-state` and `--ignore-state` are the only ways to ask for less. A new flag whose default keeps the old behavior is
   additive. The [selection](reference.md#selection), the same for sync and find: the whole share except what an
   exclude pattern matches; the built-in patterns are exactly the listed `--exclude` flags, and adding, removing or
   changing one is breaking;
   `--exclude` adds to them and `--no-default-excludes` removes them all; with one or more `--include` patterns,
   only files that an include matches, or that are in a folder an include matches, are selected; excludes always
   win over includes, whatever the order; one pattern grammar for both flags, that an excluded folder is not
   listed, and that case is ignored by lowercasing with no Unicode normalization.
2. **Runtime inputs.** The binary reads no configuration file and no environment variables of its own; every
   setting is a flag. The default `--state-dir` comes from the operating system's per-user config folder, as in
   [location](reference.md#location). Adding opt-in configuration later is additive and must not override explicit
   flags.
3. **Exit codes.** 0, 1 and 2, as in [exit codes](reference.md#exit-codes),
   [find exit codes](reference.md#find-exit-codes) and [delete exit codes](reference.md#delete-exit-codes), and which
   outcome maps to which code. For delete, kept files never change the exit code.
4. **What the commands do to the device.** Every command logs in as guest to the share `EMMC Images`. `sync` and
   `find` only read: they never create, change, rename, delete or lock anything on the share. `delete` is the only
   command that changes the share. It removes regular files that pass its proof mode and folders that the same run
   emptied, and never the share root. It never writes file contents, and never creates, renames or locks anything.
   To remove a file or folder that the share marks read-only, it may first clear that read-only attribute. The
   attribute can stay cleared if the removal then fails. A release in which `sync` or `find` changes the device, or
   in which `delete` does more than this, is breaking.

   The [proof modes](reference.md#proof-modes): by default a file is removed only when the state records its path
   with the size and mtime the device lists and the local copy's SHA-256 is the recorded one; with `--trust-state`
   the record is enough; with `--ignore-state` every selected file is. In every mode the file must be at least
   `--older-than` old and unchanged on the device just before removal, and a path the share would not receive
   exactly, or a [name clash](reference.md#name-clashes), keeps it. Without `--force`, delete changes nothing. A
   folder is removed only when it held entries before the run and the run removed every one of them, as in
   [emptied folders](reference.md#emptied-folders).
5. **The destination.** The [layout](reference.md#destination-layout): files mirror the share's tree under
   `--dest` with names kept byte for byte; downloaded files get the device's mtime, adopted files keep their own,
   which is within the adoption tolerance in the decision rules; and folders' mtimes are not set. The
   [decision rules](reference.md#how-a-pass-decides): a file recorded with the same size and mtime is never copied
   again, whether or not it is still in the destination; a changed one is downloaded again; a matching local file is
   adopted; any other local file at that path is overwritten. Sync deletes nothing in the destination except its own
   temporary files.
6. **Temporary files.** A download is written to `.seestar-copy-<hash>.tmp` in the folder of its final file, where
   `<hash>` is 64 lowercase hex digits; it never ends in `.fit`. Leftover files with exactly that form of name are
   removed at startup. See [temporary files](reference.md#temporary-files).
7. **The state file.** Its location, `<state-dir>/sync/<sha256>.jsonl`, where `<sha256>` is the lowercase hex
   SHA-256 of the absolute, cleaned `--dest`. Its [format](reference.md#format): JSON Lines, append-only; a header
   `{"v":1,"dest":"<absolute dest>"}`; then one record per line with the members `path` (share-relative, forward
   slashes), `size` (bytes), `mtime` (the device's, RFC 3339), `sha256` (lowercase hex), `at` (RFC 3339) and `how`
   (`copied`, `replaced` or `adopted`); the last record for a path wins; a record is written only after its file is
   complete under its final name. A state file written by a newer release on the same major version loads in an
   older one: a new member is optional, older releases ignore it, and readers treat an unknown `how` as unknown. A
   format that an older release cannot read gets a new `v`, and is breaking. Only one sync using the same
   destination spelling and state directory runs at a time; use the same values for every invocation. The lock
   files' names are not promised. delete never writes the state file, and takes no lock.
8. **Distribution.** `<version>` below is the release's tag, such as `v0.1.0`. Linux archives are named
   `seestar-copy_<version>_linux_<arch>.tar.gz` and Windows archives `seestar-copy_<version>_windows_<arch>.zip`,
   for amd64 and arm64. Each contains `seestar-copy-cli` (with `.exe` on Windows), LICENSE and NOTICE at the archive
   root. The macOS download is
   `seestar-copy_<version>_darwin_universal.dmg`, containing a universal `seestar-copy-cli`, LICENSE and NOTICE.
   The single release `SHA256SUMS` lists exactly these five downloads as lowercase SHA-256, two spaces, filename,
   newline. Names, contents, checksum syntax and these targets are promises. Removing a target or a program is
   breaking; adding one, such as the planned desktop app, is not. Linux and Windows CLI executables are
   byte-for-byte reproducible with identical source, version, revision and Go toolchain; verify by rebuilding and
   comparing with the executable unpacked from the archive. The Mac CLI is combined with lipo and signed, so it is
   not byte-reproducible; the DMG is verified by `SHA256SUMS` and attestation only. See
   [verifying a release](reference.md#verifying-a-release).
9. **`--version`.** One line on stdout, `seestar-copy-cli <version> (<revision>, <os>/<arch>)`, where a release's
   `<version>` is its tag, such as `v0.1.0`, and `<revision>` is the first 12 hex digits of its commit.
10. **find's output.** The listing goes to stdout, one line per entry, in listing order, and lists the folders the
    selection reads and the files it selects, as in [what find lists](reference.md#what-find-lists). The
    [text format](reference.md#text-format): its fields, their order and syntax, and when and how a path is quoted.
    The [JSONL format](reference.md#jsonl-format): the member names, their types and meanings. The three
    [statuses](reference.md#statuses) `copied`, `changed` and `new` and the rule for each. Find writes nothing on
    disk and never creates `--dest` or the state. Adding a JSONL member is additive; removing or renaming one,
    adding or changing a text field, adding a status or changing what one means is breaking.

## Not in the contract

- Log text: messages, keys, levels and their order. The [logs](reference.md#logs) table documents what a release
  logs, but a release may change it; the notes say when it does.
- Human-readable output: help text, error wording and the formatting of `licenses`.
- Go packages. Every package other than the command is under `internal/`; there is no Go API.
- scripts/, docs/development/ and the test fixtures.
- Timing and throughput: pass duration, the order files are copied in, and how reads are split on the wire. The
  meaning of `--interval`, `--ready-age` and `--first-pass-age` is in the contract; how long a pass takes is not.
- Which SMB dialect and features are negotiated with the device, as long as items 1-10 hold.

## Bumps

A release is classified by its **net change against the previous release on its line**, judged against what that
release documented. Restoring documented behavior that a release broke is a patch. Intentionally changing documented
behavior is breaking whatever the commit calls itself. Tests, refactors and doc corrections need no release of their
own and are a patch when bundled.

Before 1.0 (`0.y.z`):

- **Minor**: any contract break, and new features. The notes list every break under "Breaking", with recovery steps.
- **Patch**: everything else.

From 1.0:

- **Major**: a contract break.
- **Minor**: new features and new contract items; a change to the compatibility record that adds or drops a firmware
  line, forced by the device or by choice, with a notes entry.
- **Patch**: fixes, including fixes for device changes that change no contract item.

A security or safety fix ships on the latest line at whatever bump its contract impact requires; the label does not
waive the break notes.

**Go module major.** At v2, the module path becomes `github.com/zmerlynn/seestar-copy/v2` with matching self-imports
and install instructions. Internal packages do not remove that rule.
