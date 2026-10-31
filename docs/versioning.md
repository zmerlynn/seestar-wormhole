# Versioning and compatibility

## Principle

seestar-wormhole follows Semantic Versioning 2.0.0 over a declared **contract**. The version number promises only what
the contract lists. Anything not listed can change in any release.

The vendor's telescope firmware, phone apps and cloud service change without notice and are not covered by the number.
Each release instead carries a **compatibility record**: what was actually tested with that artifact, and what is
carried forward from earlier evidence.

## The release unit

A release is six CLI executables (linux, darwin, windows x amd64, arm64), four native UI executables
(linux and windows x amd64, arm64), one universal macOS UI DMG, and `SHA256SUMS`, built by the
release workflow from an annotated tag. The Containerfile is a build-it-yourself example with no contract. If a
container image is ever published, its executable path, entrypoint arguments, UID/GID 65532, writable `/state` and
tag scheme join the contract from that release, with the image tagged by the binary's version.

## The contract

1. **Commands and flags.** `proxy`, `discovery`, `licenses`, and GUI-only `ui`; flag names, meanings and grammar; defaults that change what is
   written or sent; and the rules about which combinations are refused.
2. **Runtime inputs.** `SEESTAR_EMAIL` and `SEESTAR_PASSWORD` and how they are interpreted. The binary discovers no config or credential file (a systemd EnvironmentFile is the service manager's,
   not ours). Adding opt-in config discovery later is additive and must not override explicit flags or variables.
3. **Installation identity.** Memory-only unless `--state-dir` is given; with it, the identity at
   `<state-dir>/InterProcessKV/device_uuid` is kept, or migrated compatibly.
4. **Exit codes.** 0, 1, 2 and 3, as in docs/reference.md "Exit codes", and which outcome maps to which code.
5. **Reasons.** One authoritative table of every `reason` and `cleanup_reason` value with its mapping to exit
   codes. Readers must treat an unknown reason as an unspecified failure and use the process exit code. A new reason for a new
   case is additive; reclassifying an already-documented case follows the fix-versus-break rule below. Hint text is
   prose and not promised.
6. **The proxy's final-failure record.** One documented JSON stderr record when the proxy exits on a classified
   failure, with a stable record name and stable `reason` and `cleanup_reason` keys. It is the only log record in the
   contract.
7. **Proxy listeners and forwarding.** Port-mapping syntax and default mappings; bind behavior; byte-transparent
   forwarding including half-close; the documented forwarding and local-answer rules (the setter table); LAN discovery on
   UDP 4720 as an app-facing protocol; session ownership, release and recovery behavior.
8. **What the proxy does to the device by default.** A new default that writes device state, or a default that stops
    protecting something it used to (a pin, a setter drop), is breaking. A new read-only request or a new startup
    refusal is minor and is listed in the notes, including its side effects on the device where known.
9. **Distribution.** Bare CLI executables are named `seestar-wormhole-cli_<version>_<os>_<arch>`
    (plus `.exe` on Windows) for linux, darwin, windows x amd64, arm64. Bare UI executables are named
    `seestar-wormhole_<version>_linux_<arch>` and `seestar-wormhole_<version>_windows_<arch>.exe`.
    The macOS UI is `seestar-wormhole_<version>_darwin_universal.dmg`, containing `seestar-wormhole.app`,
    an `/Applications` symlink, LICENSE and NOTICE. The release `SHA256SUMS` lists each download as
    lowercase SHA-256, two spaces, filename, newline. Names, contents, checksum syntax, and these targets
    are promises. Removing a target is breaking; adding one is not. CLI downloads are byte-for-byte
    reproducible with identical source, version, revision and Go toolchain; UI downloads are verified
    by SHA256SUMS and attestation only.
10. **`--version`.** An extractable version, revision and platform; the exact surrounding text is not promised.

## Not in the contract

- Log text and log record names, text or JSON, except the final-failure record (item 6).
- Human-readable output: help text and license display formatting.
- Go packages. Every package other than the command is under `internal/`; there is no Go API.
- The simulator, scripts/, docs/development/, and the Containerfile (see "The release unit").
- Timing, throughput, retry delays and jitter. The qualitative guarantees in items 7-8 (exclusive
  ownership and bounded cleanup) are not timing and stay protected.
- Wire details towards the vendor. A vendor handshake can change in a patch if items 1-10 hold.

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
  line, forced by the vendor or by choice, with a notes entry.
- **Patch**: fixes, including vendor-change fixes that change no contract item.

A security or safety fix ships on the latest line at whatever bump its contract impact requires; the label does not
waive the break notes.

**Go module major.** At v2, the module path becomes `github.com/zmerlynn/seestar-wormhole/v2` with matching
self-imports and install instructions. Internal packages do not remove that rule.
