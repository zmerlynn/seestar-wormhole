The `embed/` directory is generated; do not edit it directly. From the repository
root, run:

```sh
go run ./scripts/generate-licenses.go
go run ./scripts/generate-licenses.go --check
```

The generator checks the module/SPDX table in the project NOTICE against
`vendor/modules.txt`, copies vendored license and notice files verbatim (including
COPYRIGHT and AUTHORS), and uses `go list -deps` to verify coverage for the
current build target. It needs no network; JSON uses the standard library’s
JSON v2 packages.
The catalog includes all source-carrying vendored modules so cross-builds work
without regeneration. At runtime, `debug.ReadBuildInfo` selects the modules
linked into that binary. Module order and file order are deterministic.

Supplemental source-file notices live under `notices/<module>/`. The generator
includes them verbatim with that module's vendored notices. This preserves the
Apache-2.0 terms in go-keyring's macOS source without editing generated vendor
files; its root license covers the remaining code under MIT.
