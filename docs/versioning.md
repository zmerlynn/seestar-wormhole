# Versioning and compatibility

## Principle

seestar-wormhole follows Semantic Versioning 2.0.0 over a declared **contract**. The version number promises only what
the contract lists. Anything not listed can change in any release.

The vendor's telescope firmware, phone apps and cloud service change without notice and are not covered by the number.
Each release instead carries a **compatibility record**: what was actually tested with that artifact, and what is
carried forward from earlier evidence.

## The release unit

A release is the six archives (linux, darwin, windows x amd64, arm64) and the release `SHA256SUMS`, built by
`scripts/release.sh` from an annotated tag. The Containerfile is a build-it-yourself example with no contract. If a
container image is ever published, its executable path, entrypoint arguments, UID/GID 65532, writable `/state` and
tag scheme join the contract from that release, with the image tagged by the binary's version.

## The contract

1. **Commands and flags.** `proxy`, `pull`, `licenses`; flag names, meanings and grammar; defaults that change what is
   written or sent; and the rules about which combinations are refused.
2. **Runtime inputs.** `SEESTAR_EMAIL` and `SEESTAR_PASSWORD` and how they are interpreted; credential-free `--via`
   operation. The binary discovers no config or credential file (a systemd EnvironmentFile is the service manager's,
   not ours). Adding opt-in config discovery later is additive and must not override explicit flags or variables.
3. **Installation identity.** Memory-only unless `--state-dir` is given; with it, the identity at
   `<state-dir>/InterProcessKV/device_uuid` is kept, or migrated compatibly.
4. **Exit codes.** 0-4 and 130, as in docs/reference.md "Exit codes", and which outcome maps to which code.
5. **Machine-readable output.** The final `pull --output json` object: one object, its fields, their types and
   meanings. Readers must ignore unknown fields. Field order is not promised.
6. **Reasons.** One authoritative table of every `status`, `reason` and `cleanup_reason` value with its mapping to exit
   codes. Readers must treat an unknown reason as an unspecified failure and use `exit_code`. A new reason for a new
   case is additive; reclassifying an already-documented case follows the fix-versus-break rule below. Hint text is
   prose and not promised.
7. **The proxy's final-failure record.** One documented JSON stderr record when the proxy exits on a classified
   failure, with a stable record name and stable `reason` and `cleanup_reason` keys. It is the only log record in the
   contract.
8. **Destinations.** The destination layout; `<dest>/.seestar-wormhole/state.json` and its receipts; the pull
   `SHA256SUMS` format; the destination lock; no deletion on the device or the destination; interrupted-copy recovery.
   - A release keeps operating on destinations its predecessors in the same major accepted.
   - Automatically rewriting an existing, previously usable destination so that older supported binaries can no longer
     read it is **breaking**, even if the data files are intact or a backup would recover it.
   - A new opt-in layout in a separate destination (as explicit `--dir` with schema 5 was) is a feature. It leaves the
     old layout alone and the notes state the minimum binary that reads it.
   - The notes of a release that changes state give the schema it writes, the schemas it reads, and when it first
     writes.
   - State is an interface between the project's binaries. Third-party readers or editors of state.json are not supported.
   - Lowering a hard limit (inventory entries, state size) so that a working destination is refused is breaking.
9. **Proxy listeners and forwarding.** Port-mapping syntax and default mappings; bind behavior; byte-transparent
   forwarding including half-close; the documented forwarding and local-answer rules (the setter table); LAN discovery on
   UDP 4720 as an app-facing protocol; session ownership, release and recovery behavior.
10. **What the proxy does to the device by default.** A new default that writes device state, or a default that stops
    protecting something it used to (a pin, a setter drop), is breaking. A new read-only request or a new startup
    refusal is minor and is listed in the notes, including its side effects on the device where known.
11. **Distribution.** Archive names `seestar-wormhole_<version>_<os>_<arch>` (`.tar.gz`, `.zip` on Windows), the
    executable at the archive root, the release `SHA256SUMS` syntax, and the six targets. Removing a target is breaking;
    adding one, or adding documents to the archive, is not.
12. **`--version`.** An extractable version, revision and platform; the exact surrounding text is not promised.

## Not in the contract

- Log text and log record names, text or JSON, except the final-failure record (item 7).
- Human-readable output: help text, the text pull summary, progress rendering and cadence. Scripts use item 5.
- Go packages. Every package other than the command is under `internal/`; there is no Go API.
- Trace and keylog file formats. Their safety rules (off by default, created exclusively and privately) stay.
- The simulator, scripts/, docs/development/, and the Containerfile (see "The release unit").
- Timing, throughput, retry delays and jitter, telemetry cadence. The qualitative guarantees in items 8-10 (no
  deletion, no `--via` fallback, exclusive ownership, bounded cleanup) are not timing and stay protected.
- Wire details towards the vendor. A vendor handshake can change in a patch if items 1-12 hold.

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
  line, forced by the vendor or by choice, with a notes entry; a new opt-in destination layout.
- **Patch**: fixes, including vendor-change fixes that change no contract item.

A security or safety fix ships on the latest line at whatever bump its contract impact requires; the label does not
waive the break notes.

**Go module major.** At v2, the module path becomes `github.com/zmerlynn/seestar-wormhole/v2` with matching
self-imports and install instructions. Internal packages do not remove that rule.
