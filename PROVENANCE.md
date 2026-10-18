# JSON dependency provenance

See [dependency pin](go.mod), [attribution](NOTICE), and [JSON policy](docs/reference.md#json-file-compatibility).

Selected on October 17, 2026. The default Go module proxy's `@latest` returned
this version; `@v/list` returned no tags. The proxy's
`@v/v0.0.0-20260820222146-c27c302e5fc3.info` reports
`2026-08-20T22:21:46Z`, before the October 3 cutoff. Full upstream commit:
`c27c302e5fc3f63878d1faaf2971f132bae3b152`.

The dependency was fetched with `go get` through the default GOPROXY with
GOSUMDB enabled, then `go mod tidy` and `go mod vendor`, in Go 1.27. Production
uses the vendored implementation without `GOEXPERIMENT=jsonv2`. No duplicate
name allowance is enabled. Existing Pion, rclone, and go-smb2 vendor patches
were restored unchanged after vendoring. See [development](docs/development/README.md#vendoring-and-patches)
for their contracts and [NOTICE](NOTICE) for attribution.
