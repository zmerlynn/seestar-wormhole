# Security policy

## Reporting a vulnerability

Report security problems privately through GitHub's private vulnerability reporting:
[report a vulnerability](https://github.com/zmerlynn/seestar-copy/security/advisories/new), or open the
**Security** tab and choose **Report a vulnerability**. Do not open a public issue, pull request or discussion.

Use the same route if you find sensitive data published in this repository, its history or its releases.

Include the seestar-copy version (`seestar-copy-cli --version`), your OS, the steps to reproduce and what an attacker
gains. Leave out the things listed under [What never goes in](CONTRIBUTING.md#what-never-goes-in): no passwords,
account names, device serials, real IP addresses, coordinates or packet captures. A report that needs one of these
should describe the need without including the material; sharing is then arranged privately.

This is a one-maintainer project, handled on a best-effort basis. Reports are acknowledged in the advisory, and a fix
is released before the advisory is published.

## Supported versions

Only the latest release receives fixes.

## Scope

In scope: the `seestar-copy-cli` binary, a read-only SMB client that must never write to the telescope; its handling
of names and data from the device; what it writes in the destination folder and its state file; the release workflow
and its artifacts.

Out of scope:

- problems in the telescope, its firmware, its SMB share or the phone app; report those to the vendor;
- the share's guest login. The telescope's share takes guest logins with no password, so anyone who can reach it
  can read it; that is the device's design, and sync uses the same login;
- problems in [seestar-wormhole](https://github.com/zmerlynn/seestar-wormhole), when sync reaches the telescope
  through it; report those there.

## Verifying a release

Release checksums and build provenance verification are described in the
[command reference](docs/reference.md#verifying-a-release).
