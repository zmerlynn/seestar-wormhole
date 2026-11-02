# Contributing

## Reporting a bug

Open an issue with the seestar-copy version (`seestar-copy-cli --version`), your OS, device model and firmware version, the command line with real addresses replaced, and relevant log lines reduced to only the fields that matter. The [command reference](docs/reference.md) explains every flag, log line and exit code.

## What never goes in

Do not put any of these in an issue, a pull request or an attachment:

- passwords, tokens, keys, account names or email addresses;
- device identifiers such as serial numbers, UIDs or MAC addresses;
- real IP addresses (use the RFC 5737 documentation ranges) or site coordinates;
- packet captures, or logs that have not been reduced to the fields that matter.

## Reporting a security problem or published sensitive data

Do not report security problems or published sensitive data in a public issue. Use GitHub private vulnerability reporting: open the **Security** tab, then choose **Report a vulnerability**. See the [security policy](SECURITY.md). Problems in the device or its firmware belong with the vendor.

## Pull requests

PRs are squash-merged, and CI runs the checks on every PR. The [development guide](docs/development/README.md) is the home for building, testing and the project rules. Coding agents also read [AGENTS.md](AGENTS.md).

## License

Contributions are under this repository's [Unlicense](LICENSE). Vendored code keeps its own license; see [NOTICE](NOTICE).
