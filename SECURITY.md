# Security policy

## Reporting a vulnerability

Report security problems privately through GitHub's private vulnerability reporting:
[report a vulnerability](https://github.com/zmerlynn/seestar-wormhole/security/advisories/new), or open the
**Security** tab and choose **Report a vulnerability**. Do not open a public issue, pull request or discussion.

Use the same route if you find sensitive data published in this repository, its history or its releases.

Include the seestar-wormhole version (`seestar-wormhole-cli --version`), your OS, the steps to reproduce and what an
attacker gains. Leave out the things listed under [What never goes in](CONTRIBUTING.md#what-never-goes-in): no
passwords, tokens, account names, device serials or UIDs, real IP addresses, coordinates or packet captures. A report
that needs one of these should describe the need without including the material; sharing is then arranged privately.

This is a one-maintainer project, handled on a best-effort basis. Reports are acknowledged in the advisory, and a fix
is released before the advisory is published.

## Supported versions

Only the latest release receives fixes.

## Scope

In scope: the `seestar-wormhole` and `seestar-wormhole-cli` binaries and Containerfile, its handling of credentials, cloud and device traffic,
and its local listeners; the release workflow and its artifacts.

Out of scope:

- problems in the telescope, its firmware, the phone app or the vendor's cloud services; report those to the vendor;
- the documented lack of authentication on the proxy's local listeners. Anyone who can reach them can control the
  telescope, so bind them only to a network you trust; see [local network trust](docs/reference.md#proxy).

## Verifying a release

Release checksums and build provenance verification are described in the
[command reference](docs/reference.md#verifying-a-release).
