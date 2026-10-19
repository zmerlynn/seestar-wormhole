# Contributing

## Reporting a bug

Open an issue with the seestar-wormhole version (`seestar-wormhole --version`), your OS, device model and firmware version, the command line with secrets removed, and relevant log records reduced to only the fields that matter. See [Troubleshooting](docs/troubleshooting.md) and the [command and log reference](docs/reference.md) for existing guidance.

## What never goes in

Do not put any of these in an issue, a pull request or an attachment:

- passwords, tokens, keys, account names or email addresses;
- device identifiers such as serial numbers, UIDs or MAC addresses;
- real IP addresses (use the RFC 5737 documentation ranges) or site coordinates;
- packet captures, or logs that have not been reduced to the fields that matter.

## Reporting a security problem or published sensitive data

Do not report security problems or published sensitive data in a public issue. Use GitHub private vulnerability reporting: open the **Security** tab, then choose **Report a vulnerability**. Problems in the device, app or vendor services belong with the vendor.

## Clean-room rule

This project is built from observation of the developer's own telescope and from public information. Protocol work uses only these inputs: the public protocol spec and its linked binary-derived evidence, repository material, and public library documentation and source. Do not read existing implementations of these protocols or fill gaps from memory of another implementation.

## Pull requests

PRs are squash-merged, and CI runs the tests on every PR. The [development guide](docs/development/README.md) is the home for building, testing and conventions.

## License

Contributions are under this repository's [Unlicense](LICENSE). Vendored code keeps its own license; see [NOTICE](NOTICE).
