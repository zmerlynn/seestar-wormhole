# How to hack on it

seestar-copy copies the files from a ZWO Seestar's SMB share to a local
folder. Its command-line tool, `seestar-copy-cli`, has one main command,
`sync`. It reads the telescope's share read-only, mirrors the files under a
destination folder, and keeps a state file of what it has copied, so a file
moved out of the destination is not copied again. It repeats passes until it
is stopped.

- [Architecture](#architecture)
- [Build and check](#build-and-check)
- [Releasing](#releasing)
- [Project rules](#project-rules)
- [Reviewing a change](#reviewing-a-change)

## Architecture

| Path | What it does |
| --- | --- |
| `cmd/seestar-copy-cli` | The command tree (built with [cobra](https://github.com/spf13/cobra)), flag checks, exit codes and logging, the read-only guard before a dry run, `find`'s listing, and `browse`'s listener and `--open`. |
| `cmd/seestar-copy` | The app's command: `--settings`, logging, and how the page is shown. The `gui` build shows it in a [Wails](https://wails.io) window, with native folder and save dialogs, and quits the app when the window closes. The build without the tag prints the page's URL to open in a browser and serves until Ctrl+C. |
| `internal/app` | The app: `app.json` (read at startup, written atomically on Save), and the loopback server for its page, with a per-run token and Host, Origin and Fetch Metadata checks, Setup, Settings, Test, and the Browse tab, which is `internal/browse` in its in-app mode. |
| `internal/engine` | The work the commands share: the `--exclude` and `--include` selection and its walk of the share, the `sync` pass loop (dial, list, plan, copy, log off, sleep) and its dry run, and `delete`'s run. It never turns the read-only guard on. |
| `internal/remote` | The read-only view of the telescope's share through [go-smb2](https://github.com/hirochachacha/go-smb2): guest login, recursive listing, read-only opens. `Remover`, which only `delete` dials, adds removing a regular file or an empty folder after rechecking it. `NewFake` serves an `io/fs` tree with injected faults for tests, and `NewFakeDir` a local folder that is also a `Remover`. |
| `internal/state` | The per-destination state file: JSON Lines, last record per path wins, torn-tail tolerance, append with fsync, and the exclusive lock. |
| `internal/plan` | The pass decisions: wait, done, copy, replace, adopt or fail for each listed file, and `delete`'s decisions on files and emptied folders. Pure functions with no I/O; the current time is an input. |
| `internal/copier` | Carries out a pass's decisions over the one session with a worker pool: temporary file beside the final one, hash, size check, fsync, mtime, rename, then the state record. |
| `internal/browse` | `browse`'s web server: the HTTP handler with its Host, Fetch Metadata and path checks, the embedded page, and the pool that shares one SMB session between requests, closing it when idle, failed or stuck. |
| `internal/readonly` | The dry-run guard: once enabled, every function that changes the local filesystem or the device panics. A test checks that each such function calls it first. |
| `internal/licenses` | The `licenses` command's output: the project's `NOTICE` and `LICENSE`, then the license texts of each module linked into the running build. |
| `scripts/generate-licenses.go` | Writes the license catalog that `internal/licenses` embeds. |

The command keeps one rule from the start: a usage error is reported before
anything touches the network or the disk.

- Help, `--version` and `licenses` write to stdout.
- Logs and errors are `log/slog` text lines with UTC timestamps on stderr.
- A usage error is one line naming the option, the rule it broke, and the help
  command to run. It exits 2. Any other failure exits 1.

`--version` prints `seestar-copy-cli <version> (<revision>, <os>/<arch>)`.
Release builds set the version and a 12-character revision with
`-ldflags "-X main.version=... -X main.revision=..."`. Without them, the
revision comes from Go's VCS build stamp, or is `development`.

### License catalog

`internal/licenses/embed/` is generated; don't edit it by hand. After changing
dependencies, `NOTICE` or `LICENSE`, run from the repository root:

```sh
go run ./scripts/generate-licenses.go
go run ./scripts/generate-licenses.go --check
```

The generator asks `go list -deps` which modules each release target links,
for both commands and for the app with and without its window. It copies their
license and notice files verbatim from `vendor/`, adds the supplemental notices
under `internal/licenses/notices/<module>/` for source-file terms upstream
keeps in no separate file, and refuses to run if a module has no license file
or no SPDX entry in the generator. `NOTICE` must contain the module table the
generator prints when it is out of date. At run time, `licenses` uses the
executable's build information to print only the modules linked into that
build. `NOTICE`'s last section, on the GTK and WebKitGTK libraries the Linux
window links, is embedded apart and printed only by that build.

## Build and check

The tool is written in Go 1.27 or newer and builds with `CGO_ENABLED=0` for
Windows, macOS and Linux on amd64 and arm64. Dependencies are pinned in
`go.mod`/`go.sum` and checked into `vendor/`; builds use `-mod=vendor`.

`./scripts/check.sh` runs the checks in the `docker.io/library/golang:1.27`
image with Podman, so no Go is needed on the host:

1. Pull the image once: `podman pull docker.io/library/golang:1.27`.
2. Download and verify the pinned modules into the module cache, the only
   step that uses the network: `./scripts/check.sh prepare`.
3. Run the checks with no network: `./scripts/check.sh`.

The checks are in `scripts/check-go.sh`, which CI runs too: formatting,
build, cross-builds and vet for every release target, the license catalog
check, the race tests, and the release tooling tests. The build and module
caches live under `${XDG_CACHE_HOME:-$HOME/.cache}/seestar-copy/`; set
`SEESTAR_COPY_BUILD_CACHE` or `SEESTAR_COPY_MODULE_CACHE` to move them.

`./scripts/test-smb.sh` runs the `internal/remote` tests against a Samba
fixture, a guest-only, read-only share like the telescope's, built from
`internal/remote/testdata/samba`. Building the fixture image downloads
Samba; the tests run on a private Podman network. CI runs them too. They
need the `smbintegration` build tag and `SEESTAR_COPY_SMB_ENDPOINT`
(`host:port`) naming the fixture.

`test/browser` holds browser tests of the `browse` page, written with
[Playwright](https://playwright.dev/) and run in Chromium and WebKit. CI
runs them in the `browser` job. They cover the listing and its count,
folder navigation with Back, Forward and breadcrumb focus, thumbnails, the
image, text and FITS viewers, Save, closing the viewer, a missing folder, a
long name in a narrow window, and the favicon. They also save screenshots
of the page in light and dark, which CI uploads. The app's tests start the
browser-only `seestar-copy` themselves, each with its own settings file, and
cover Setup, Settings and their field errors, Test's messages, the tabs, and
the Browse tab, including a new address replacing it, and the layout at
200% text, wider text spacing and 480 px wide. The tests need the Samba
fixture's `browse` target, a tree of real files from `make-browse.sh`
instead of `tree.txt`'s. To run them locally, with Node.js and Podman:

```sh
podman build --target browse -t seestar-copy-samba-browse internal/remote/testdata/samba
podman run -d --rm --name seestar-copy-samba-browse -p 127.0.0.1:14450:14450 seestar-copy-samba-browse
go build -mod=vendor ./cmd/seestar-copy-cli
go build -mod=vendor ./cmd/seestar-copy
./seestar-copy-cli browse --endpoint 127.0.0.1:14450
```

browse prints its URL. In another terminal:

```sh
cd test/browser
npm ci && npx playwright install chromium webkit
BROWSE_URL=http://127.0.0.1:PORT/ APP_BIN=../../seestar-copy npx playwright test
```

Use the URL browse printed for `BROWSE_URL`. Screenshots go to
`test/browser/screenshots`. Stop the fixture with
`podman stop seestar-copy-samba-browse`.

With Go 1.27 installed, the same checks run directly:

```sh
GOFLAGS=-mod=vendor sh scripts/check-go.sh
```

To build the command:

```sh
go build -mod=vendor ./cmd/seestar-copy-cli
```

`main` changes only through a pull request. The optional pre-push hook
rejects pushes to `main`. It is enabled once per clone with:

```sh
git config core.hooksPath .githooks
```

## Releasing

A `v*` tag builds the release: Linux and Windows archives, a signed and
notarized universal Mac DMG, and `SHA256SUMS`, in a draft GitHub Release.
[Cutting a release](releasing.md) describes the build mechanics, the Mac
signing secrets, rehearsing with a manual run, and checking that the
archives are reproducible.

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
  Markdown. JSON goes through the standard library's `encoding/json` into
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
  `readonly.Check`; `internal/readonly`'s test fails otherwise.
- **Commit to the new path.** Don't keep a fallback to old behavior unless a
  named configuration still needs it.
- **Files on disk stay downgradable.** A state file written by a newer
  release must still load in an older one. Add optional members with a
  documented default for when they are absent, and never rewrite a file just
  because it was read. The format's version changes only for a change an
  older release cannot read, and such a one-way change needs the
  maintainer's approval before it is written.
- **Every public flag, format and exit code is a promise**, as
  [versioning](../versioning.md) lists. Add one, or a log line, only for a
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

[`.claude/skills/seestar-copy-review`](../../.claude/skills/seestar-copy-review/SKILL.md)
describes how to review a change in focused lanes, such as correctness, tests,
simplicity, licensing and docs. A coding agent can follow it as a skill, and a
person can follow it as a checklist. A review is scoped to the change by
default and ends in a single verdict.
