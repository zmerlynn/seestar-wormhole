# Review lanes

Each section is one lane's checklist. A lane stays in its concern; other lanes
cover the rest. Every lane follows the scope rule and output format in
[SKILL.md](SKILL.md).

Two rules cut across every lane:

- **Never write to the device.** The tool opens files on the share read-only
  and does nothing else to it. Any create, delete, rename, truncate, write,
  set-info, lock or open with write access on the share is a Must fix in
  whichever lane finds it.
- **The state file never lies.** A file is recorded in the state only after
  it is complete on disk under its final name. A skipped or failed file is
  always logged; a file that is only not ready yet is counted in the pass
  summary instead.

## correctness

Is the changed code right, and robust where it reads untrusted input?

1. Read the change commit by commit, then as a whole. For each changed
   function, state its contract: inputs, outputs, errors, side effects. Check
   every caller the change affects, including callers outside the diff.
2. Untrusted input means the device's listings and file contents, and the
   state file on disk. Sizes and counts are checked before use. The project
   is strict in what it writes and permissive in what it accepts, through the
   decoder's own options
   ([project rules](../../../docs/copy/development/README.md#project-rules)).
   Write a focused test for any input you suspect, and report what it did.
3. Copy ordering. Walk the crash points of one file: folder created, temp
   file created, bytes written and hashed, byte count checked against the
   listed size, fsync, close, mtime set, rename over the final path, the
   folder fsynced (not on Windows, a documented gap), then its state line
   appended and fsynced. A crash or error at any step leaves either no
   state line or a complete file under its final name. An adoption only
   reads the file, rechecks it after hashing, and records it. A file
   renamed into place before a cancel is still recorded. A short read is a
   failure, not a smaller file.
4. State file. The last line for a path wins. A final line without a newline,
   or one that does not parse, is dropped with a warning; any other bad line
   is fatal. Appends after a torn tail start on a fresh line and do not glue
   onto the torn bytes. A header that names another destination is refused.
5. Pass decisions. Every documented rule for a file in a pass (not ready
   yet, already copied, changed on the device, adopted, new) maps to one
   branch in `internal/copy/plan/`, and the destination is consulted only for
   adoption.
6. Error paths. Every error is handled, wrapped with context or returned;
   none is swallowed. A failed file is logged and left for the next pass;
   other files continue. A missing source is logged as a warning and does
   not fail the pass. A failed connection or any other listing failure
   fails the pass.
7. Teardown. Every opened session, share, remote file, local file, goroutine
   and timer is closed on every path, including early returns and
   cancellation. Temp files are removed on failure and on cancel.
8. Compatibility. Existing users keep their behavior: default sources,
   existing state files, existing flags. A change in behavior is deliberate
   and documented.
9. Mutate the two or three most important new conditions on a scratch copy
   and run the package tests.

Must fix: a write to the device, wrong behavior on a reachable input, a state
line for a file not fully on disk, data loss, a crash, a resource leaked on a
common path, or a silent compatibility break. A robustness weakness on an
unreachable input is Should fix at most.

## concurrency

Concurrency, cancellation and lifetime in the changed code.

1. For every goroutine the change starts or touches: who owns it, what stops
   it, and what waits for it. A goroutine that can outlive its owner, or
   block forever on a send or receive, is a finding. The worker pool exits
   before the session logs off.
2. Channels. Each one has a documented lifetime. When closure signals
   completion, one owner closes it exactly once and no send follows the
   close. A channel may remain unclosed when all users exit without leaving
   blocked senders or receivers. Buffer sizes are justified. Select loops
   that need cancellation honor `ctx.Done()`.
3. Shared state. Name every field read or written from more than one
   goroutine, and the lock or atomic that guards it. State appends from
   several workers are serialized, and each line reaches the file whole. No
   lock is held across network I/O unless documented.
4. Contexts and timers. Deadlines propagate. Check timer ownership and
   intended timeout behavior. Go 1.27 garbage collects unreferenced timers;
   a leak finding needs evidence of retained references or other live work.
   Cancellation cannot race with completion into a double report, a lost
   error, or a state line for a canceled file.
5. Cancellation mid-copy. A SIGINT or SIGTERM during a read cancels in-flight
   reads, removes their temp files, logs off and exits 0. Files finished
   before the signal are in the state; files cut off are not. The sleep
   between passes wakes on the signal.
6. Evidence. Cite the race tests from the shared checks. For a suspected
   race, write a targeted stress test and run it with `-race -count=50` or
   more; report the command and the result.

Must fix: a data race, a deadlock, a goroutine leaked per pass or per file,
a torn or interleaved state line, or a shutdown that leaves a canceled file
in the state or skips log-off.

## tests

Do the tests pin the changed behavior?

1. Map each added or altered behavior to the test that would fail without
   it, and list behaviors that have no test. A contract in
   `docs/copy/reference.md` that the change touches needs a test.
2. Mutation. Apply at least five mutations to the changed non-test lines on
   a scratch copy, then run only the affected packages. Useful mutations:
   - flip a condition;
   - drop an error return;
   - move a bound off by one;
   - skip a cleanup or an fsync;
   - reorder two writes, especially the rename and the state append.

   Report each mutant as killed or survived.
3. Quality. Assertions check exact values, not just `err != nil`. State-file
   fixtures are literal bytes, independent of the encoder under test,
   including a torn final line and a bad middle line. Tests do not depend on:
   - wall time (the plan takes `now` as an input);
   - map order;
   - the network or a real telescope;
   - machine-local files outside the checkout;
   - each other.

   Keep mutable test files in `t.TempDir()`. Read-only checked-in fixtures
   may be read directly from `testdata/`.

   Run the touched packages with `-count=3 -shuffle=on`.
4. The copy and sync paths are exercised against the `io/fs`-backed fake
   remote: short reads, an error mid-file, cancel mid-file, a rename over an
   existing file, names Windows cannot store, and several passes end to end.
5. New decoders of untrusted input, such as the state-file loader, get a
   fuzz target with seeds checked in.
6. Any weakened assertion needs a stated reason.

Must fix: a surviving mutant on a correctness-relevant line, a changed
contract with no test, or a flaky test.

## simplicity

YAGNI: what current requirement needs each addition, today? Every public flag,
option, exit code, log line and file format is a promise that must be kept
after release. Every internal abstraction is code someone has to read.

1. Inventory what the change adds:
   - flags and options;
   - exported identifiers, interfaces, types, generic helpers, packages;
   - goroutines;
   - files on disk, state record fields;
   - log lines.

   For each, name the requirement that needs it now: an issue, a documented
   behavior, a user need. If there is none, propose deleting it.
2. Look for:
   - interfaces with one implementation (the remote interface has two: the
     SMB client and the test fake);
   - options nobody sets, or parameters always passed the same value;
   - wrappers that only forward;
   - configurability added "for later";
   - retry loops inside a pass (there is one attempt per operation; the next
     pass is the retry);
   - duplicated logic, or one function doing two jobs;
   - error types nobody inspects;
   - abstractions made before the second use.
3. Look for work the standard library or an existing dependency already
   does: a hand-written JSON reader or writer, a path walker beside `io/fs`,
   an SMB detail go-smb2 already handles.
4. Prefer fewer modes: no two code paths for one outcome, no flag that only
   changes defaults, no legacy path kept just in case. Commit to the new path
   unless a named configuration needs the old one.
5. Readability. A reader new to the package can follow the change. Names say
   what things are, and comments say why rather than what.

Output a Delete / Merge / Inline list, giving each item the lines it removes or
the shape it becomes, then the findings. Must fix is only for a new public
promise with no current requirement, because that cannot be taken back after
release. Everything else is Should fix or Nit.

## operability-security

The threat model:
- the local network is trusted;
- the device is trusted, but its listings are untrusted input for path
  safety;
- the binary is public and run by people who are not developers.

1. Read-only device. Every open on the share asks for read access only, and
   no other call can change the share. Search the changed paths for every
   go-smb2 call and name each one.
2. Paths. A name from the device cannot escape the destination: reject `..`
   elements, absolute paths, empty elements, backslashes and drive letters,
   and any joined path that is not inside the destination after cleaning.
   Share paths use forward slashes; local paths are built with
   `path/filepath` only at the last step. A rejected name is a logged
   per-file failure.
3. Windows names. A name Windows cannot store (`: * ? " < > |`, a trailing
   dot or space, a reserved device name such as `CON` or `LPT1`) fails that
   file with a clear error each pass and is never renamed silently. Which
   platforms apply the rule is documented and tested.
4. Local files. Temp files (`.seestar-copy-<hash>.tmp`) live in the folder
   of their final file, so the rename stays in one folder, and leftovers
   anywhere under the destination are removed at startup: only regular
   files with exactly that name, never through a symbolic link. A device
   file with such a name fails as a reserved name. A rename over an existing
   file works on Windows. The state file is never inside the destination, is
   created with user-only permissions, and only one sync writes it at a time.
5. Budgets. Every loop over the listing, every allocation sized by a listed
   size or a state-file line, and every timeout is bounded, and the bound is
   documented. A listed size is checked against the bytes actually read.
6. Diagnosability. A failure the change can produce gives the user a line
   they can act on: which file, what failed, what happens next. Log levels
   are right, with no per-file chatter beyond the one line per copied file.
   The guest login means there are no secrets, but a credential, a host
   name or a device serial still does not reach the log by accident.
7. Defaults are safe: the tool never sends the device a write, a command,
   or a configuration change, whatever the flags.

Must fix: any write to the device, a path escape, a state line for an
incomplete file, an unbounded allocation from remote input, or an unsafe
default.

## cli-contract

`docs/copy/reference.md` is the contract for scripts and careful users; help text
is the contract for people at a terminal.

1. Build the head binary and compare help, `docs/copy/reference.md` and behavior
   for everything the change adds or alters:
   - flags, arguments and defaults;
   - exit codes;
   - the per-file lines and the per-pass summary line;
   - `licenses` and `--version`.

   Show the commands you ran.
2. Compatibility. An existing invocation keeps its output and exit code
   unless the change deliberately breaks it, and then the reference says so.
3. Error surfaces. Each of these produces the documented exit code and a
   message a non-developer can act on:
   - invalid or conflicting flags, or a missing `--endpoint` or `--dest`;
   - a destination that cannot be created;
   - a state file for another destination, or one already in use;
   - an unreachable endpoint (with `--once`).
4. Output stays parseable and stable, and each stream goes where the
   reference says. Logs go to stderr.
5. Naming follows the existing style: kebab-case flags, units in names
   where needed, and no new name that collides with or shadows another.
6. Path flags behave on Windows and macOS: separators, drive letters, case
   and a relative `--dest`.

Must fix: help, reference and behavior disagree; an undocumented
compatibility break; or a wrong exit code.

## upgrade

Can a user move between releases in both directions without losing data or
behavior? The rule is in
[project rules](../../../docs/copy/development/README.md#project-rules): files on
disk stay downgradable. The file that matters is the per-destination state
file; losing it means copying a whole night again, or copying files the user
already moved away.

1. Inventory every file the change reads or writes outside the destination
   tree (the state file, its lock file), and for each its location, its
   version member, its members and their defaults when absent.
2. Upgrade. A state file written by the base release loads at HEAD with the
   same meaning. Write it as a literal-byte fixture taken from the base, not
   by marshaling HEAD's struct.
3. Downgrade. A state file appended to at HEAD loads in the base release.
   Build the base and feed it HEAD's file, or show by reading why it would
   load. New members in a record are optional and ignored by the base.
4. Versions. The header's `v` changes only for a change an older release
   cannot read, and the reference names it. An unknown or newer version is
   refused with the documented error and the file is never appended to or
   rewritten. Loading never rewrites the file.
5. Location. A change to how the state path is derived from the destination
   (cleaning, case, symlinks, drive letters) is a change of format: an
   existing user's state must still be found.
6. Defaults. A changed default, a renamed or retyped member, or a changed
   meaning of an existing value (`how`, `mtime`, `path`) is either compatible
   with old files or a one-way change.
7. Names follow the file's existing convention (short lowercase JSON
   members) and common forms: numbers as numbers, times in RFC 3339 UTC,
   paths share-relative with forward slashes.

Must fix: data loss on upgrade or downgrade; a state file that an older
release can no longer read without the maintainer's recorded approval; a file
appended to after it was refused; a state file no longer found after an
upgrade; a one-way change the reference does not name. Before the format's
first release these are Should fix unless they would ship.

## licensing

The project's own code is under the Unlicense (`LICENSE`); `NOTICE` lists
everything else.

1. For every module added, removed or upgraded, read its license file in
   `vendor/` and identify it by SPDX id. Check it is compatible with
   distributing one static binary:
   - MIT, BSD, Apache-2.0 and ISC are fine with a notice, and an Apache-2.0
     `NOTICE` file must be carried;
   - anything else, including MPL-2.0, LGPL, GPL, AGPL, SSPL, or an unknown
     or absent license, is a Must fix unless the maintainer approved it.
2. Verify that `vendor/modules.txt` is consistent with `go.mod`, that no
   `replace` directive points at a local path, and that every `replace` to a
   fork names a tag whose diff from upstream is documented. The SMB library
   is pinned by pseudo-version; a change to that pin is reviewed here and in
   tests.
3. The built binary's `licenses` command includes every linked module, and
   any generated license set is current.
4. `NOTICE` is accurate for the change.
5. New code that looks lifted carries a license and attribution. Signs are a
   break in style, a foreign copyright line, or a comment naming another
   project.
6. Non-code assets have a known author and terms.
7. ZWO and Seestar are named only descriptively, consistent with the
   not-affiliated paragraph.

Must fix: an incompatible or unidentified license, a missing notice or
attribution, or a stale embedded license set.

## build-release

1. Build-tag and `GOOS`-specific changes compile with `CGO_ENABLED=0` on
   every release target: windows, darwin and linux, each on amd64 and arm64.
   The cross-builds in `./scripts/check.sh` are a minimum.
2. Code that behaves differently on Windows (rename over an existing file,
   reserved names, path separators, file locking) has a test that runs on
   every target's logic, or a stated reason why it cannot.
3. Release scripts, when they exist: archive names, contents, checksums and
   the embedded version are right, and a rebuilt binary matches the release.
4. Workflows pin actions by SHA, grant minimal permissions, keep secrets out
   of logs, and do what the release docs say.
5. Scripts run from a clean checkout, with no network beyond what they
   declare. Builds use `-mod=vendor`.
6. `go.mod`'s `go` and `toolchain` lines match the documented toolchain and
   the CI image.
7. Someone following `docs/copy/development/README.md` from scratch can build and
   test.

Must fix: a broken build on a release target, a release that isn't
reproducible or is assembled wrong, or a CI change that grants new
permissions or leaks secrets.

## public-hygiene

The repository is public. Review everything the change adds to the tree,
including file names. The change must not add any of the following:

- secrets, tokens or credentials;
- device serials, device addresses or account data;
- email addresses other than the author's;
- real coordinates or place names that locate a user;
- absolute home paths, machine names, or temp and worktree paths;
- references to private documents, private repositories or private
  processes;
- file listings, logs or fixtures from a real device;
- work notes addressed to reviewers.

Use a scanner for the patterns, then read the diff once by eye for what
patterns miss. Do not repeat a sensitive value in the report: give the file,
the line and the category. Any true positive is a Must fix.

## docs-editorial

An editor, not a proofreader: accuracy first, then whether the reader gets
what they came for. It covers Markdown, help strings, and the errors and
messages a user reads.

The readers:
- `README.md` is for Seestar owners who are not developers;
- `docs/copy/reference.md` owns the mechanics: flags, defaults, exit codes, the
  state file and the log lines;
- `docs/copy/development/` is for contributors.

1. Accuracy. Every claim in the changed text is true at HEAD. Build the
   binary and run each example shown that needs no telescope, then compare
   names, defaults, output and exit codes with help and the reference. A
   false claim or a broken example is a Must fix.
2. Consistency. One thing has one name everywhere. Guides link to the
   reference for mechanics instead of restating them: a restated fact that
   disagrees is a Must fix, and one that only duplicates is a Should fix.
   Check links and anchors with a Markdown parser, never a regex. List
   external links; don't fetch them.
3. Audience and structure. A page leads with what the reader can do. Steps
   are numbered, one action each, with prerequisites first. The README stays
   short and sends detail to `docs/`. Flag jargon the reader won't know.
4. Voice: plain and dry, in the voice the page already uses. No marketing,
   emoji, exclamation marks or filler hedges. Personal details stay vague.
5. Style:
   - American spelling;
   - consistent capitalization of Seestar and ZWO;
   - code, paths and flags in backticks.
6. Required text stays: the not-affiliated paragraph and the license
   pointers.
7. Help strings and messages are short and use the reference's terms. An
   error says what failed and what to do next.

Give a before/after rewrite of the exact text for every finding, so the fix
can be applied as written. Must fix: an inaccuracy, a broken example or link,
or required text removed. Should fix: a real comprehension problem. Nit:
style.
