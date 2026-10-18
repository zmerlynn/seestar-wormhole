---
name: wormhole-review
description: Review a seestar-wormhole change (a branch, a commit range or chosen areas) in focused lanes - correctness, concurrency, tests, simplicity, operability and security, provenance, CLI contract, licensing, build and release, public hygiene, docs editorial - scoped to what the change touches, ending in one verdict.
---

# Reviewing a seestar-wormhole change

A review splits one change into lanes. Each lane looks at one concern, reads
only the files that concern touches, and reports findings in the same shape.
The lane checklists are in [lanes.md](lanes.md). Read
[docs/development/README.md](../../../docs/development/README.md) first; it
has the architecture, the checks and the project rules a reviewer applies.

## 1. Fix the scope

The default scope is the change: `git diff BASE..HEAD`. For a branch, BASE is
its merge base with `main`. Write down BASE and HEAD as full SHAs and review
those, not names that can move.

- A finding must be anchored in a changed line, or in a contract the change
  alters (a caller, a doc, a format that now behaves differently).
- A problem the change neither introduces nor worsens goes under "Out of
  scope": at most three per lane, never Must fix.
- A wider review is a deliberate choice: name the areas (for example "pull
  and cli") and review the current state of their files instead of the diff.

Commit messages are not reviewed; history is squashed before publishing.

## 2. Choose the lanes

Read the diff stat and the diff, then pick lanes by what changed. This is a
judgment call; the table is a starting point.

| Lane | Run it when the change touches |
|---|---|
| correctness | any non-test Go |
| concurrency | goroutines, channels, locks, atomics, contexts or timers; anything in `internal/edge/` or `internal/proxy/` |
| tests | any Go, including tests alone |
| simplicity | any non-test Go, or a new flag, option or file format |
| operability-security | logging, secrets, crypto, file writes, budgets; `internal/cloud/`, `internal/edge/`, `internal/proxy/`, `internal/pull/`, diagnostics, protocol packages |
| provenance | protocol packages (`internal/core/`, `internal/iotc/`, `internal/rdt/`, `internal/p2ptunnel/`, `internal/dtls/`, `internal/session/`), `internal/edge/`, `internal/cloud/` |
| cli-contract | `cmd/`, `docs/reference.md`, help text, exit codes, log records |
| licensing | `go.mod`, `go.sum`, `vendor/`, `LICENSE`, `NOTICE`, `internal/licenses/`, new imports, new data files, copied code |
| build-release | `scripts/`, `Containerfile`, `.github/`, build tags, `GOOS`-specific code |
| public-hygiene | always |
| docs-editorial | Markdown, help strings, user-facing errors and messages |

A docs-only change is usually docs-editorial plus public-hygiene. A
dependency upgrade is licensing, build-release and tests at least. Skip a lane
only when nothing in it applies, and say which lanes you skipped.

## 3. Run the checks once

Run the repository checks once for the whole review and let every lane cite
them, instead of each lane rebuilding:

```sh
./scripts/check.sh            # gofmt, build, macOS cross-builds, vet, race tests
./scripts/check.sh fuzz       # when a fuzz target or a decoder changed
go test -race scripts/release.go scripts/release_test.go   # when scripts/ changed, in the Go container
```

`./scripts/check.sh prepare` must have run once with the network. Lanes that
need more (a mutation, a stress test, a built binary to compare with the docs)
work on a scratch copy in their own container, never on the checkout.

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
