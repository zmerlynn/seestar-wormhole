# The copy packages

The copier, which came from seestar-copy, copies the files from a ZWO
Seestar's SMB share to a local folder. Its main command-line form is
`seestar-wormhole-cli sync`, beside `find`, `browse` and `delete`. It reads the telescope's
share read-only, mirrors the files under a destination folder, and keeps a
state file of what it has copied, so a file moved out of the destination is
not copied again. It repeats passes until it is stopped.

- [Architecture](#architecture)
- [Build and check](#build-and-check)
- [Releasing](#releasing)
- [Project rules](#project-rules)
- [Reviewing a change](#reviewing-a-change)

## Architecture

| Path | What it does |
| --- | --- |
| `internal/copy/cli` | The file commands (built with [cobra](https://github.com/spf13/cobra)): their flags and checks, the copy history folder, `find`'s listing, and `browse`'s listener and `--open`. |
| `cmd/seestar-wormhole` | Mounts the file commands under `seestar-wormhole-cli` and the app's executable (`files.go`), and gives them the shared `--log-format` and `--log-level`, the stderr logger, usage errors, exit codes, and the read-only guard before a dry run. |
| `internal/copy/app` | The Copy, Browse and Free up space tabs' server, which seestar-wormhole's app (`internal/ui`) mounts under `/copy/api/` and `/browse/`: the copy, preview and free-space controllers, Settings validation, Test, the activity log, and the Browse tab, which is `internal/copy/browse` in its in-app mode. The app keeps the settings in its `copy.json` and tells the mount where the telescope is. |
| `internal/copy/engine` | The work the commands share: the `--exclude` and `--include` selection and its walk of the share, the `sync` pass loop (dial, list, plan, copy, log off, sleep) and its dry run, and `delete`'s run. It never turns the read-only guard on. |
| `internal/copy/remote` | The read-only view of the telescope's share through [go-smb2](https://github.com/hirochachacha/go-smb2): guest login, recursive listing, read-only opens. `Remover`, which only `delete` dials, adds removing a regular file or an empty folder after rechecking it. `NewFake` serves an `io/fs` tree with injected faults for tests, and `NewFakeDir` a local folder that is also a `Remover`. |
| `internal/copy/state` | The per-destination state file: JSON Lines, last record per path wins, torn-tail tolerance, append with fsync, and the exclusive lock. |
| `internal/copy/plan` | The pass decisions: wait, done, copy, replace, adopt or fail for each listed file, and `delete`'s decisions on files and emptied folders. Pure functions with no I/O; the current time is an input. |
| `internal/copy/copier` | Carries out a pass's decisions over the one session with a worker pool: temporary file beside the final one, hash, size check, fsync, mtime, rename, then the state record. |
| `internal/copy/browse` | `browse`'s web server: the HTTP handler with its Host, Fetch Metadata and path checks, the embedded page, and the pool that shares one SMB session between requests, closing it when idle, failed or stuck. |
| `internal/copy/guard` | The panic guard: the copier's goroutines, including Browse's idle and cancellation timers and the reads of a multipart Browse response, recover panics, log only where they were recovered (no panic value or stack), and finish or retire their work before returning, so a copier bug can't end the proxy that shares its process. |
| `internal/copy/readonly` | The dry-run guard: once enabled, every function that changes the local filesystem or the device panics. A test checks that each such function calls it first. |
| `internal/licenses` | Shared with seestar-wormhole: the `licenses` command's output, the project's `NOTICE`, then the license texts of each module linked into the running build. |
| `scripts/generate-licenses.go` | Writes the license catalog that `internal/licenses` embeds. |

The commands keep one rule from the start: a usage error is reported before
anything touches the network or the disk.

- Help, the root's `--version` and `licenses` write to stdout.
- Logs and errors are `log/slog` lines with UTC timestamps on stderr: text by
  default, JSON with `--log-format json`. Lines lost to a stalled or failed
  stderr are counted and make the command exit 1; they never go to stdout.
- A usage error is one text line naming the option, the rule it broke, and
  the help command to run, whatever `--log-format` says. It exits 2. Any
  other failure exits 1.

The file commands take no `--version`; `seestar-wormhole-cli --version`
reports the build, with the version and revision that release builds set with
`-ldflags "-X main.version=... -X main.revision=..."`.

### License catalog

The license catalog is seestar-wormhole's: `internal/licenses` and
`scripts/generate-licenses.go`, described in the
[development guide](README.md). The generator checks
that every module `seestar-wormhole` and `seestar-wormhole-cli` link has a
license in the catalog and a row in `NOTICE`. Run from the repository root:

```sh
go run ./scripts/generate-licenses.go
go run ./scripts/generate-licenses.go --check
```

## Build and check

The tool is written in Go 1.27 or newer and builds with `CGO_ENABLED=0` for
Windows, macOS and Linux on amd64 and arm64. Dependencies are pinned in
`go.mod`/`go.sum` and checked into `vendor/`; builds use `-mod=vendor`.

`./scripts/check.sh` runs the repository's checks, seestar-copy's included,
in the `docker.io/library/golang:1.27` image with Podman, so no Go is needed
on the host. See the [development guide](README.md) for its
`prepare` step and caches.

`check.sh` and CI also cross-build and vet the copy commands and packages
for every target with `CGO_ENABLED=0`, and vet the Samba tests.

`./scripts/test-smb.sh` runs the `internal/copy/remote` and
`internal/copy/app` Samba tests against a fixture, a guest-only, read-only
share like the telescope's, built from `internal/copy/remote/testdata/samba`;
see the [development guide](README.md#optional-integration-checks).
CI runs them too. They need the `smbintegration` build tag and
`SEESTAR_COPY_SMB_ENDPOINT` (`host:port`) naming the fixture.

`test/browser` holds browser tests of the `browse` page and the app's
page, written with [Playwright](https://playwright.dev/) and run in Chromium
and WebKit. CI runs them in the `copy-browser` job. For `browse` they cover
the listing and its count, folder navigation with Back, Forward and
breadcrumb focus, thumbnails, the image, text and FITS viewers, Save,
closing the viewer, a missing folder, a long name in a narrow window, and
the favicon. `app.spec.js`, `copy.spec.js` and `free.spec.js` cover the
app's Connection on this network, Copy with real passes, Browse, Free up
space, the tabs, the token, and a folder in use by `seestar-wormhole-cli sync`
or a second app. They start the page without a window from `internal/ui`'s test
binary: its `TestBrowserServer` serves the real page and runtime from the
state folder `SEESTAR_WORMHOLE_BROWSER_STATE` names, with no account and no
proxy, and prints the page's URL. The tests also save screenshots in light
and dark, which CI uploads. They need the Samba fixture's `browse` target,
a tree of real files from `make-browse.sh` instead of `tree.txt`'s. To run
them locally, with Node.js and Podman:

```sh
podman build --target browse -t seestar-copy-samba-browse internal/copy/remote/testdata/samba
podman run -d --rm --name seestar-copy-samba-browse -p 127.0.0.1:14450:14450 seestar-copy-samba-browse
go build -mod=vendor -o seestar-wormhole-cli ./cmd/seestar-wormhole
go test -mod=vendor -c -o bin/ui.test ./internal/ui
./seestar-wormhole-cli browse --endpoint 127.0.0.1:14450
```

browse prints its URL. In another terminal:

```sh
cd test/browser
npm ci && npx playwright install chromium webkit
BROWSE_URL=http://127.0.0.1:PORT/ UI_TEST_BIN=$PWD/../../bin/ui.test CLI_BIN=$PWD/../../seestar-wormhole-cli npx playwright test
```

Use the URL browse printed for `BROWSE_URL`. Screenshots go to
`test/browser/screenshots`. Stop the fixture with
`podman stop seestar-copy-samba-browse`.

To build the commands:

```sh
go build -mod=vendor -o seestar-wormhole-cli ./cmd/seestar-wormhole
```

## Releasing

The file commands ship in `seestar-wormhole-cli`, in every seestar-wormhole
archive and in the DMG; see [releasing](releasing.md). Releases before 0.7.0
shipped them as a separate `seestar-copy-cli`.

## Project rules

These apply to every change, whoever writes it.

- **Only `delete` changes the telescope, and only through
  `remote.Remover`.** Everything else opens the share read-only and never
  creates, deletes, renames, changes or locks anything on it.
- **Strict in what you write, permissive in what you read.** Everything the
  tool writes, and every safety check on input, is strict: sizes checked
  before use, no allocation from unchecked values, names from the device
  checked before they become local paths. Merely unexpected input is
  tolerated: unknown JSON members, missing optional members, unknown values
  carried as unknown.
- **Standard formats use standard libraries.** Don't hand-write a parser,
  walker or tokenizer for a format that has a well-known library: JSON, SMB,
  Markdown. JSON goes through the standard library's `encoding/json/v2` into
  typed structs holding only the members used; permissiveness comes from the
  decoder's own behavior, so never turn on rejecting unknown members. SMB
  goes through the vendored client library. If it isn't clear whether a
  library covers a format, ask in an issue before writing a parser. This
  applies to tests and tooling too.
- **The state file never lies.** A file is recorded only after it is
  complete on disk under its final name. A file that is skipped or fails is
  always logged; one that is only not ready yet is counted in the pass
  summary.
- **A fix starts with a test that fails without it.** Prefer literal-byte
  fixtures to round trips, and check that the test catches a mutation of the
  line it protects.
- **A dry run writes nothing.** It reads the state and the destination
  through views that have no write methods, and every function that creates,
  writes, renames or removes a local file or folder, or removes one on the
  device, starts with
  `readonly.Check`; `internal/copy/readonly`'s test fails otherwise.
- **Commit to the new path.** Don't keep a fallback to old behavior unless a
  named configuration still needs it.
- **Files on disk stay downgradable.** A state file written by a newer
  release must still load in an older one. Add optional members with a
  documented default for when they are absent, and never rewrite a file just
  because it was read. The format's version changes only for a change an
  older release cannot read, and such a one-way change needs the
  maintainer's approval before it is written.
- **Every public flag, format and exit code is a promise**, as
  [versioning](../versioning.md#the-contract) lists. Add one, or a log line, only for a
  current need, and document it in the reference in the same change.
- **Dependencies are few, vendored and Unlicense-compatible.** The project
  is under the Unlicense. A dependency must allow distributing one static
  binary with a notice: MIT, BSD, Apache-2.0 or ISC. The vendor tree is
  exactly what `go mod tidy` and `go mod vendor` produce, and vendored files
  are never edited by hand.
- **Keep work product out of the tree.** File listings, logs and review
  notes stay outside the checkout.
- **Documentation:** American spelling. The reference owns mechanics; guides
  link to it rather than restating it.

## Reviewing a change

A copy change is reviewed with the app's
[`.claude/skills/wormhole-review`](../../.claude/skills/wormhole-review/SKILL.md);
its lane table names the copy packages, and its lanes carry the copy rules.
See [Reviewing a change](README.md#reviewing-a-change) in
the development guide.
