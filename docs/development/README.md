# How it works and how to hack on it

seestar-wormhole turns one cloud-connected telescope session into local TCP
connections. The phone app, control clients, and SMB downloads share that
session. Start with the [command reference](../reference.md) if you want to run
it; this page is for changing it.

All application packages live under `internal/`. They support the command and
simulator within this module; there is no Go API for external consumers.

- [Follow a connection](#follow-a-connection)
- [Build and run](#build-and-run)
- [Offline validation](#offline-validation)
- [Test layout and evidence](#test-layout-and-evidence)
- [Vendoring](#vendoring)
- [Licenses and generated notices](#licenses-and-generated-notices)
- [Project rules](#project-rules)
- [Reviewing a change](#reviewing-a-change)

## Follow a connection

See [architecture and ownership](../../DESIGN.md#layering-and-ownership).

### Keep decisions separate from I/O

See [core ownership and buffers](../../DESIGN.md#layering-and-ownership).

Failures cross adapters as classified values. Normalize once, then forward the
same result. Preserve quiet/canceled receipts at their own phase or as typed
receipts; wrapping them as a new operation failure changes attribution.
[internal/diagnostic](../../internal/diagnostic) projects bounded classifications
without formatting private dependency errors. Do not log raw action envelopes,
cloud bodies, credentials, or forwarded payloads.

## Build and run

Run commands from the repository root. You need Go 1.27 or newer. Dependencies
are pinned in `go.mod`/`go.sum` and checked into `vendor/`.

```sh
CGO_ENABLED=0 go build -mod=vendor -o seestar-wormhole ./cmd/seestar-wormhole
./seestar-wormhole --version
./seestar-wormhole proxy --help
```

Use `GOOS`/`GOARCH` for cross-builds. The container script builds Linux amd64
and arm64; macOS and Windows have platform-specific source for both architectures. For example, on a build host:

```sh
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -mod=vendor -p 2 -o seestar-wormhole ./cmd/seestar-wormhole
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=vendor -p 2 -o seestar-wormhole.exe ./cmd/seestar-wormhole
```

See [runtime site rules](../reference.md#site-protection).

`main` changes only through a pull request. The optional pre-push hook
rejects pushes to `main`. It is enabled once per clone with:

```sh
git config core.hooksPath .githooks
```

Release archives for every platform come from `scripts/release.sh`; see
[cutting a release](releasing.md).

## Offline validation

The validation scripts use Podman and `docker.io/library/golang:1.27`.
The image must already be present for offline runs. The scripts require at
least 6 GiB available host memory, give the container 4 GiB/two CPUs, and limit
Go/package parallelism to two. They use the image's toolchain with
`GOTOOLCHAIN=local`; checks never download a replacement toolchain.

```sh
./scripts/check.sh prepare  # network-enabled dependency download and verification
./scripts/check.sh          # offline formatting, build, macOS cross-builds, vet, race tests
./scripts/check.sh fuzz     # offline bounded fuzz sessions
./scripts/test-container.sh # final image users and restricted native startup
./scripts/test-check.sh     # script command contract, without running Go
```

`prepare` mounts source read-only and runs `go mod download` and `go mod
verify`. Repeat it after dependency changes. Check/fuzz use `--network=none`,
`GOPROXY=off`, and `GOSUMDB=off`. They use vendored source; the prepared module
cache also supplies the selected toolchain.

The normal check rejects non-test `encoding/json` imports, verifies gofmt, builds all packages, cross-builds both macOS
architectures and compiles their CLI/discovery tests, runs vet, then runs
`go test -race -count=2 -timeout 50m ./...`. macOS test compilation is not
execution. Go/package parallelism is two throughout.

Fuzz mode runs every target for 60 seconds with two workers.
`./scripts/test-check.sh` checks the campaign against the source declarations
to catch omitted targets:

| Package | Targets |
|---|---|
| `cmd/seestar-wormhole` | `FuzzConfiguration` |
| `internal/ui` | `FuzzSettings` |
| `internal/iotc` | `FuzzLicenseJSON`, `FuzzPacket`, `FuzzStream`, `FuzzProtocolParsers`, `FuzzLicense`, `FuzzWireEvents`, `FuzzMetamorphicReceive` |
| `internal/cloud` | `FuzzJSON`, `FuzzAccountResponses`, `FuzzDevices`, `FuzzOutcomeJoin` |
| `internal/cloud/cloudhttp` | `FuzzCloudMetamorphic`, `FuzzRejectedResponseDiagnostics` |
| `internal/edge/iotcedge` | `FuzzIOTCLicenseVerification` |
| `internal/proxy` | `FuzzSiteProtection` |
| `internal/landiscovery` | `FuzzDecodeRequest` |
| `internal/edge` | `FuzzDTLSAdmission` |
| `internal/dtls` | `FuzzControlBytes` |
| `internal/rdt` | `FuzzDecode`, `FuzzMachine`, `FuzzMetamorphicReceive` |
| `internal/p2ptunnel` | `FuzzDecode`, `FuzzStream`, `FuzzMetamorphicReceive` |

Caches default outside the worktree at
`$XDG_CACHE_HOME/seestar-wormhole/{go-build,go-mod}`, or
`$HOME/.cache/seestar-wormhole/{go-build,go-mod}`. You can override them with
`SEESTAR_WORMHOLE_BUILD_CACHE` and `SEESTAR_WORMHOLE_MODULE_CACHE`; keep both
outside the checkout. Those are script controls, not runtime application options.

CI uses the runner's `curl` and `jq` to check the Go release series in `go.mod`
(preferring `toolchain` when present) against the supported stable releases at
`go.dev/dl/?mode=json`. This check, `./scripts/check-go-support.sh`, requires
network access. Weekly Vulncheck also fails when that series is
the older supported series, giving time to upgrade, and after 45 days without a
default-branch commit, giving time to prevent GitHub's 60-day schedule shutdown.
`./scripts/test-go-support.sh` tests the support check offline with synthetic responses.

### Optional integration checks

- `./scripts/test-smb.sh` runs a disposable Samba fixture and checksum-verified
  official rclone v1.75.2, exercising guest listing/copy/download comparison
  through the composed proxy and detecting same-size corruption. It needs
  network access to install fixture tools; ordinary `go test ./...` leaves
  this environment-gated test off.
- `./scripts/test-lan-address-changes.sh <evidence-directory>` runs prefix,
  address replacement, and 64-interface flood regressions in privileged Linux
  containers. It prepares a digest-pinned Alpine image and public packages
  with network access; compilation and namespace checks use no network.
  Podman must support privileged network namespaces. Host interfaces are
  untouched. Put evidence outside the checkout.

## Test layout and evidence

| Location | What you can check there |
|---|---|
| `internal/cloud/`, `internal/cloud/cloudhttp/` | Response parsing, account lifecycle, catalog selection, allocation/release and cancellation |
| `internal/iotc/`, `internal/rdt/`, `internal/p2ptunnel/`, `internal/session/` | Codecs, virtual-clock machines, bounds, loss/reordering, retry/flow policy, stale completions |
| `internal/edge/`, `internal/edge/iotcedge/`, `internal/dtls/` | Profile checks, Pion integration, datagram boundaries, worker cleanup |
| `internal/proxy/` | Composed sessions, listeners, forwarding, busy/idle recovery, site protection and cleanup |
| `cmd/seestar-wormhole/` | CLI output/exits, startup, diagnostics and cancellation |
| `internal/landiscovery/` | Scan decoding, synthesized replies, source/interface policy and rate bounds |

The [tunnel contract](../../DESIGN.md#tunnel-contract) describes the retained stream behavior.

Most tests sit beside their package. `testdata/` holds codec examples,
metamorphic cases, and deterministic traces. Virtual-clock simulators do not
contact the network. Edge/listener tests use local loopback sockets; they do
not log into the vendor cloud or require a telescope.

Literal-byte assertions complement round trips: an encoder and decoder can
agree on the same wrong layout. Synthetic fixtures establish behavior under
the conditions they model, not acceptance by every backend or firmware.
Keep protocol behavior and local policy distinct when adding tests.

For current device/platform coverage, use [status](../status.md).

## Vendoring

Build from the checked-in vendor tree with `-mod=vendor`. The vendor tree is
exactly what `go mod tidy` and `go mod vendor` produce; CI regenerates it and
fails on any difference, so vendored files are never edited by hand.

The [Windows discovery dependency](../reference.md#lan-discovery) is carried
as an x/net fork.
The `replace` directive in `go.mod` pins a tagged fork release, so module and
vendoring commands resolve it through the module proxy like any other
dependency. The fork retains the `golang.org/x/net` module path and BSD license.

## Licenses and generated notices

See [NOTICE](../../NOTICE) for terms and [license generator notes](../../internal/licenses/README.md) for generation and integrity details.

## Project rules

These apply to every change, whoever writes it.

- **Strict in what you send, permissive in what you accept.** Everything the
  client emits, and every safety check on input, is strict: bounds and lengths
  before use, no allocation from unchecked fields, authentication before
  interpretation. Merely unexpected input is tolerated: unknown JSON fields,
  unknown enum values (carried as unknown), nonzero reserved bytes, unknown
  records skipped by length, missing optional fields. Receive diagnostics follow
  the [log contract](../reference.md#logs).
- **Standard formats use standard libraries.** Don't hand-write a parser,
  walker or tokenizer for a format that has a well-known library: JSON, HTTP,
  SMB, TLS and DTLS, Markdown. JSON goes through the standard library’s
  `encoding/json/v2` and `encoding/json/jsontext` into typed structs holding
  only the fields used, behind a size-limited reader;
  permissiveness comes from the decoder's own behavior, so never set
  `RejectUnknownMembers(true)`. Hand-written codecs are for the vendor's binary
  framing only. If it isn't clear whether a library covers a format, ask in
  an issue before writing a parser. This applies to tests and tooling too.
- **A fix starts with a test that fails without it.** Prefer literal-byte
  fixtures to round trips, and check that the test catches a mutation of the
  line it protects.
- **Commit to the new path.** Don't keep a fallback to old behavior unless a
  named configuration still needs it.
- **Files on disk stay downgradable.** A settings or state file written by a
  newer release must still load in an older one, and an older release that
  saves it must not lose what the newer one wrote. Add optional members with a
  documented default for when they are absent, keep unknown members on a
  rewrite, and never rewrite a file just because it was read. The format's
  version changes only for a change an older release cannot read, and such a
  one-way change needs the maintainer's approval before it is written.
- **Every public flag, format, exit code and log record is a promise.** Add
  one only for a current need, and document it in the
  [reference](../reference.md) in the same change.
- **Keep work product out of the tree.** Captures, logs and
  review notes stay outside the checkout; `.gitignore` blocks the common
  names.
- **Documentation:** American spelling. The reference owns mechanics; guides
  link to it rather than restating it.

## Reviewing a change

[`.claude/skills/wormhole-review`](../../.claude/skills/wormhole-review/SKILL.md)
describes how to review a change in focused lanes, such as correctness, tests,
simplicity, licensing and docs. A coding agent can follow it as a skill, and a
person can follow it as a checklist. A review is scoped to the change by
default and ends in a single verdict.

## Releasing

[`.claude/skills/wormhole-release`](../../.claude/skills/wormhole-release/SKILL.md)
is the release checklist: gates, a reproducible build, notes with a
compatibility record, the tag and a checked draft, then one owner check
before publication. [Cutting a release](releasing.md) has the build mechanics.
