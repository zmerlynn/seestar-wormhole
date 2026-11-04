---
name: seestar-copy-review
description: Review a seestar-copy change (a branch, a commit range or chosen areas) in focused lanes - correctness, concurrency, tests, simplicity, operability and security, CLI contract, upgrade, licensing, build and release, public hygiene, docs editorial - scoped to what the change touches, ending in one verdict.
---

# Reviewing a seestar-copy change

A review splits one change into lanes. Each lane looks at one concern, reads
only the files that concern touches, and reports findings in the same shape.
The lane checklists are in [lanes.md](lanes.md). Read
[docs/development/README.md](../../../docs/development/README.md) first; it
has the checks and the project rules a reviewer applies.

## 1. Fix the scope

The default scope is the change: `git diff BASE..HEAD`. For a branch, BASE is
its merge base with `main`. Write down BASE and HEAD as full SHAs and review
those, not names that can move.

- A finding must be anchored in a changed line, or in a contract the change
  alters (a caller, a doc, a format that now behaves differently).
- A problem the change neither introduces nor worsens goes under "Out of
  scope": at most three per lane, never Must fix.
- A wider review is a deliberate choice: name the areas (for example "state
  and copy") and review the current state of their files instead of the diff.

Commit messages are not reviewed; history is squashed before publishing.

## 2. Choose the lanes

Read the diff stat and the diff, then pick lanes by what changed. This is a
judgment call; the table is a starting point.

| Lane | Run it when the change touches |
|---|---|
| correctness | any non-test Go |
| concurrency | goroutines, channels, locks, atomics, contexts, timers or signals; anything in `internal/copy/` or the pass loop in `internal/engine/` |
| tests | any Go, including tests alone |
| simplicity | any non-test Go, or a new flag, option or file format |
| operability-security | logging, file writes, paths from the device, budgets; `internal/remote/`, `internal/copy/`, `internal/state/` |
| cli-contract | `cmd/`, `docs/reference.md`, help text, exit codes, log lines |
| upgrade | the state file or the lock file, their format, version or location, or their reader and writer; a change of default behavior an existing user would notice |
| licensing | `go.mod`, `go.sum`, `vendor/`, `LICENSE`, `NOTICE`, the `licenses` command, new imports, new data files, copied code |
| build-release | `scripts/`, `.github/`, build tags, `GOOS`-specific code |
| public-hygiene | always |
| docs-editorial | Markdown, help strings, user-facing errors and messages |

`internal/plan/` is pure decision logic: correctness, tests and simplicity
cover it. A docs-only change is usually docs-editorial plus public-hygiene. A
dependency upgrade is licensing, build-release and tests at least. Skip a lane
only when nothing in it applies, and say which lanes you skipped.

## 3. Run the checks once

When `./scripts/check.sh` exists, run it once for the whole review and let
every lane cite the results:

```sh
./scripts/check.sh            # gofmt, build, cross-builds, vet, race tests
```

Until then, record that the script is unavailable and run any applicable
checks that exist. The current tree contains only documentation, so no Go
build or test applies.

Lanes that need more (a mutation, a stress test, a built binary to compare
with the docs) work on a scratch copy in their own container, never on the
checkout. Nothing in a review connects to a telescope.

## 4. Run the lanes

Give each lane the scope from step 1, its file list from step 2, the check
results from step 3, its section of [lanes.md](lanes.md), and the output
format below. Lanes are independent: run them in parallel (separate agents or
reviewers) or one after another.

Every lane works read-only. It proposes fixes and does not commit them.

## 5. Lane output

Each lane writes a report outside the checkout:

- The first line is `PASS` or `REWORK`. It is REWORK if and only if there is a Must fix.
- Then Must fix, Should fix and Nit. Each finding gives:
  - `file:line`;
  - the concrete failure or cost;
  - the fix;
  - the confidence: confirmed by a test, a mutation or a reading, or only plausible.
- Then what the lane checked and found sound, and finally Out of scope.

A finding without a file, a failure and a fix is padding; leave it out. An
empty list is a good result. A lane may also write the findings as JSON, with
a JSON library, if a tool will merge them.

## 6. Merge into a verdict

1. Collect the findings, merge duplicates by `file:line` and sort them by
   severity.
2. Verify every Must fix yourself before acting on it, because a lane can be
   wrong. Downgrade a finding with a stated reason if it does not hold.
3. Give the verdict: MERGE if no Must fix survives, REWORK otherwise.
4. A rework request quotes the surviving findings with their fixes, so the
   author can apply them without the lane reports.
