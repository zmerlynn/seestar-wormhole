The `embed/` directory is generated; do not edit it directly. From the repository
root, run:

```sh
go run ./scripts/generate-licenses.go
go run ./scripts/generate-licenses.go --check
```

The generator checks the module/SPDX table in the project NOTICE against
`vendor/modules.txt`, copies vendored license and notice files verbatim (including
COPYRIGHT and AUTHORS), and uses `go list -deps` to verify coverage for the
current build target. It needs no network; JSON uses the pinned, vendored JSON v2 library.
The catalog includes all source-carrying vendored modules so cross-builds work
without regeneration. At runtime, `debug.ReadBuildInfo` selects the modules
linked into that binary. Module order and file order are deterministic.

`internal/edge/PION-LICENSE` accompanies the Pion DTLS entry. The sddl entry also carries
`supplemental/GPL-3.0.txt`, fetched verbatim from
https://www.gnu.org/licenses/gpl-3.0.txt, SHA-256
`3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986`.
LGPL-3.0 incorporates this GPL-3.0 text by reference.
