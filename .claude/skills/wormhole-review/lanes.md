# Review lanes

Each section is one lane's checklist. A lane stays in its concern; other lanes
cover the rest. Every lane follows the scope rule and output format in
[SKILL.md](SKILL.md).

## correctness

Is the changed code right, and robust where it reads untrusted input?

1. Read the change commit by commit, then as a whole. For each changed
   function, state its contract: inputs, outputs, errors, side effects. Check
   every caller the change affects, including callers outside the diff.
2. Untrusted input means anything from the device, the cloud, the network, a
   file on the share or a state file on disk. Lengths and bounds are checked
   before use, and counts are budgeted. The project is strict in what it sends
   and permissive in what it accepts, through the decoder's own options
   ([project rules](../../../docs/development/README.md#project-rules)).
   Write a focused test or fuzz seed for any input you suspect, and report
   what it did.
3. Error paths. Every error is handled, wrapped with context or returned;
   none is swallowed. Partial work is undone or recorded. Files that a reader
   may race are written by temp-and-rename or exclusive create. State on disk
   survives a crash at every step; walk the crash points.
4. Teardown. Every opened connection, file, goroutine and timer is closed on
   every path, including early returns, cancellation and panics in callees.
5. Compatibility. Existing users keep their behavior: default layouts,
   existing state files, existing flags. A change in behavior is deliberate
   and documented.
6. Mutate the two or three most important new conditions on a scratch copy
   and run the package tests.

Must fix: wrong behavior on a reachable input, data loss, a crash, a resource
leaked on a common path, or a silent compatibility break. A robustness
weakness on an unreachable input is Should fix at most.

## concurrency

Concurrency, cancellation and lifetime in the changed code.

1. For every goroutine the change starts or touches: who owns it, what stops
   it, and what waits for it. A goroutine that can outlive its owner, or
   block forever on a send or receive, is a finding.
2. Channels. Each one has a documented lifetime. When closure signals
   completion, one owner closes it exactly once and no send follows the
   close. A channel may remain unclosed when all users exit without leaving
   blocked senders or receivers. Buffer sizes are justified. Select loops
   that need cancellation honor `ctx.Done()`.
3. Shared state. Name every field read or written from more than one
   goroutine, and the lock or atomic that guards it. Check lock ordering
   across packages. No lock is held across network I/O or a callback unless
   documented.
4. Contexts and timers. Deadlines propagate. Check timer ownership and
   intended timeout behavior. Go 1.27 garbage collects unreferenced timers;
   a leak finding needs evidence of retained references or other live work.
   Reuse or stop timers and tickers when their intended lifetime requires it;
   justify allocation concerns with measurements. Cancellation cannot race
   with completion into a double report or a lost error.
5. Shutdown under load: an interrupt mid-transfer, a peer that stops
   reading, a device that drops the session. The session release and cleanup
   still run.
6. Evidence. Cite the race tests from the shared checks. For a suspected
   race, write a targeted stress test and run it with `-race -count=50` or
   more; report the command and the result.

Must fix: a data race, a deadlock, a goroutine leaked per connection or per
file, or a shutdown that skips release.

## tests

Do the tests pin the changed behavior?

1. Map each added or altered behavior to the test that would fail without
   it, and list behaviors that have no test. A contract in
   `docs/reference.md` that the change touches needs a test.
2. Mutation. Apply at least five mutations to the changed non-test lines on
   a scratch copy, then run only the affected packages. Useful mutations:
   - flip a condition;
   - drop an error return;
   - move a bound off by one;
   - skip a cleanup;
   - reorder two writes.

   Report each mutant as killed or survived.
3. Quality. Assertions check exact values, not just `err != nil`. Fixtures
   are literal bytes or literal files, independent of the encoder under test.
   Tests do not depend on:
   - wall time;
   - map order;
   - the network;
   - machine-local files outside the checkout;
   - each other.

   Keep mutable test files in `t.TempDir()`. Read-only checked-in fixtures
   may be read directly from `testdata/`.

   Run the touched packages with `-count=3 -shuffle=on`.
4. New decoders of untrusted input get a fuzz target with seeds checked in.
5. Protocol changes are exercised end to end against `internal/sim/` where the
   simulator supports them.
6. Any weakened assertion needs a stated reason.

Must fix: a surviving mutant on a correctness-relevant line, a changed
contract with no test, or a flaky test.

## simplicity

YAGNI: what current requirement needs each addition, today? Every public flag,
option, exit code, log record and file format is a promise that must be kept
after release. Every internal abstraction is code someone has to read.

1. Inventory what the change adds:
   - flags, options, config keys;
   - exported identifiers, interfaces, types, generic helpers, packages;
   - goroutines;
   - files on disk, schema fields;
   - log records.

   For each, name the requirement that needs it now: an issue, a documented
   behavior, a user need. If there is none, propose deleting it.
2. Look for:
   - interfaces with one implementation;
   - options nobody sets, or parameters always passed the same value;
   - wrappers that only forward;
   - configurability added "for later";
   - duplicated logic, or one function doing two jobs;
   - error types nobody inspects;
   - abstractions made before the second use.
3. Look for work the standard library or an existing dependency already
   does, especially a hand-written parser for a format that has a library.
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
- input from the device and the cloud is not;
- the binary is public and run by people who are not developers.

1. Secrets and identifiers must not reach logs, errors, state files or
   diagnostics. That covers keys, license material, tokens, credentials, full
   session identifiers, device serials and account data. Search the changed
   paths, run the relevant tests with debug logging, and read what is
   emitted.
2. Crypto uses library primitives only, constant-time comparison for
   secrets, and `crypto/rand` for anything secret. No verification is
   weakened.
3. Files and paths. Names from the device or the network cannot escape the
   destination, through `..`, absolute paths, symlinks, or Windows reserved
   names and separators. State and diagnostic files get the right permissions
   and are written atomically.
4. Budgets. Every loop over remote input, every allocation sized by remote
   input, and every retry and timeout is bounded, and the bound is
   documented.
5. Diagnosability. A failure the change can produce gives the user a record
   they can act on, and `docs/troubleshooting.md` names it. Log levels are
   right, with no chatty info logging in a hot path.
6. Defaults are safe:
   - listeners bind to loopback unless documented otherwise;
   - the tool commands no device motion, network, power, firmware or
     configuration change without an explicit request.

Must fix: a secret or identifier leaked into output, a path escape, an
unbounded allocation from remote input, or an unsafe default.

## provenance

Protocol behavior has the provenance the README's "About this project"
paragraph claims: observed behavior and public information.

1. Every changed protocol constant, field layout, state transition, timer
   value or message sequence is described in `DESIGN.md` or the package
   README, and the description matches the code.
2. Behavior left open is a question in `QUESTIONS.md`, not a silent guess.
   A local policy is labeled as policy, not presented as protocol.
3. Nothing reads as copied from vendor code, a decompilation or another
   implementation. Watch for:
   - unexplained magic tables;
   - identifiers no document explains;
   - comments naming another project's internals.
4. Fixtures are synthetic. Nothing is a real capture, a real key or a real
   device identity.

Must fix: protocol behavior with no description, a description that
disagrees with the code, or anything that looks copied or captured.

## cli-contract

`docs/reference.md` is the contract for scripts and careful users; help text
is the contract for people at a terminal.

1. Build the head binary and compare help, `docs/reference.md` and behavior
   for everything the change adds or alters:
   - flags, arguments and defaults;
   - exit codes;
   - summary fields and JSON fields;
   - log records.

   Show the commands you ran.
2. Compatibility. An existing invocation keeps its output and exit code
   unless the change deliberately breaks it, and then the reference says so.
3. Error surfaces. Each of these produces the documented exit code and a
   message a non-developer can act on:
   - invalid or conflicting flags;
   - a missing destination;
   - an unreachable device.
4. Machine-readable output stays parseable and stable, and each stream goes
   where the reference says.
5. Naming follows the existing style: kebab-case flags, units in names
   where needed, and no new name that collides with or shadows another.
6. Path flags behave on Windows and macOS: separators, drive letters and
   case.

Must fix: help, reference and behavior disagree; an undocumented
compatibility break; or a wrong exit code.

## licensing

The project's own code is under the Unlicense (`LICENSE`); `NOTICE` lists
everything else.

1. For every module added, removed or upgraded, read its license file in
   `vendor/` and identify it by SPDX id. Check it is compatible with
   distributing one static binary:
   - MIT, BSD, Apache-2.0 and ISC are fine with a notice, and an Apache-2.0
     `NOTICE` file must be carried;
   - MPL-2.0 is file-level;
   - LGPL needs the relinking path `NOTICE` describes;
   - GPL, AGPL, SSPL, or an unknown or absent license is a Must fix.
2. Verify that `vendor/modules.txt` is consistent with `go.mod`, that no
   `replace` directive points at a local path, and that every `replace` to a
   fork names a tag whose diff from upstream is documented, as in
   [Vendoring and patches](../../../docs/development/README.md#vendoring-and-patches).
   CI's vendor job regenerates the tree; nothing under `vendor/` is hand-edited.
3. `go run ./scripts/generate-licenses.go --check` passes. The built
   binary's `licenses` command includes every linked module.
4. `NOTICE` is accurate for the change. That covers new third-party code,
   adapted code (the Pion suite adapter is the model: upstream license kept,
   patch documented) and any LGPL component's obligations.
5. New code that looks lifted carries a license and attribution. Signs are a
   break in style, a foreign copyright line, or a comment naming another
   project.
6. Non-code assets have a known author and terms.
7. Release archives contain `LICENSE` and `NOTICE`.
8. ZWO and Seestar are named only descriptively, consistent with the
   not-affiliated paragraph.

Must fix: an incompatible or unidentified license, a missing notice or
attribution, or a stale embedded license set.

## build-release

1. Build-tag and `GOOS`-specific changes compile on every release target
   (`scripts/release.go` lists them). The macOS cross-builds in
   `./scripts/check.sh` are a minimum.
2. Release script and test:
   - the archive layout, names and checksums are right;
   - the archive contents match the file-set test;
   - the build is reproducible: two builds of one commit are byte-identical;
   - the embedded version is correct.
3. Workflows pin actions by SHA, grant minimal permissions, keep secrets out
   of logs, and do what `docs/development/releasing.md` says.
4. `Containerfile` and the scripts run from a clean checkout, with no network
   beyond what they declare.
5. `go.mod`'s `go` and `toolchain` lines match the documented toolchain and
   the CI image.
6. Someone following `docs/development/README.md` from scratch can build and
   test.

Must fix: a broken build on a release target, a release that isn't
reproducible or is assembled wrong, or a CI change that grants new
permissions or leaks secrets.

## public-hygiene

The repository is public. Review everything the change adds to the tree,
including file names. The change must not add any of the following:

- secrets, keys, tokens, license material or credentials;
- device serials, device or session identifiers, or account data;
- email addresses other than the author's;
- real coordinates or place names that locate a user. The documented
  sample coordinates are synthetic; check any new number pair that could be
  a latitude and longitude;
- absolute home paths, machine names, or temp and worktree paths;
- references to private documents, private repositories or private
  processes;
- captures, logs or fixtures from a real device or account;
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
- each `docs/` guide is for the same readers doing one task;
- `docs/reference.md` owns the mechanics: flags, defaults, exit codes and
  formats;
- `DESIGN.md` and `docs/development/` are for contributors.

1. Accuracy. Every claim in the changed text is true at HEAD. Build the
   binary and run each example shown, then compare names, defaults, output
   and exit codes with help and the reference. A false claim or a broken
   example is a Must fix.
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
