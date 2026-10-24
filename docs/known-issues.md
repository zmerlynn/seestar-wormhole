# Known issues and accepted limits

The maintainer considers every item on this page minor. These are known,
accepted, low-impact limits; none blocks normal use. Reports that raise them
without new evidence will be closed with a link to this page.

## Security and exposure

- A transport-failed login can retry indefinitely, with the existing equal-jitter
  backoff capped at 15–30 seconds. Each request has a deadline; see [availability
  and retry behavior](reference.md#availability-and-busy-devices).
- Site and admission warnings are emitted per event, and startup retry details
  can remain buffered until startup resolves. Per-connection logs and the
  unauthenticated listener trust boundary are documented in [local network
  trust](reference.md#proxy) and [SECURITY.md](../SECURITY.md#scope).
- The shared 128 client slots have no active-stream idle timeout, and rapid
  connection churn can temporarily exhaust channel IDs within their five-minute
  retention period. See [flow control](reference.md#flow-control) for the
  limits and operator guidance.

## Robustness

- HTTP 429, 502, 503 and 504 use the existing capped backoff; `Retry-After` is
  ignored. Allocation and stop retain their ownership safeguards; see
  [availability and retry behavior](reference.md#availability-and-busy-devices).
- License override selection sorts names and uses the first ten, rather than
  preserving document order. This compatibility choice is described in
  [protocol deviations](protocol.md#deliberate-deviations).
- The fuzz guard finds `func FuzzX` declarations by pattern. An unusual comment
  between `func` and the name, or a target under a dot-directory, is not found;
  gofmt and `./...` make both cases unlikely.
- Unix ancillary-data parsing tolerates short or duplicate packet-info
  messages. This leniency is deliberate; discovery still applies its
  application-level admission and reply checks.

## Dependencies and release

- The Go support checks used by CI and scheduled maintenance need `go.dev` to
  be reachable.
  Toolchain and container entry points are not identical, and an offline
  container build can require the exact image toolchain; see the
  [Go support check](development/README.md#offline-validation).
- The weekly vulnerability check's dormancy alarm fails an Actions run after
  inactivity. It does not independently monitor delivery, recipients or
  Dependabot pauses.
- A supported-Go update does not update deployed copies: rebuilding, releasing
  and reinstalling the binary is manual; see [supported
  versions](../SECURITY.md#supported-versions) and the [system service
  setup](always-on.md#a-systemd-service).
- The x/net fork remains pinned while upstream CLs [853705](https://go.dev/cl/853705)
  and [853706](https://go.dev/cl/853706) are pending, and needs maintenance until then. Its Windows
  successful-send-count workaround goes once the minimum supported Go version
  includes the fix. The fork's CI was run on a
  child commit that changed workflow triggers only; there has been no native
  Windows arm64 run. See [Windows discovery](reference.md#windows).

## Platforms and testing

- macOS was tested natively by the maintainer on real hardware before the
  current Linux host. Native Windows is CI-only, with no real-device or LAN run;
  always-on idle release and redial over days has not been run on firmware 9.75. See
  [tested coverage](status.md).

## Deliberate scope choices

- The cloud, authentication and telescope-protocol dependency chain is retained
  because it provides the remote connection and safe session ownership. Vendor
  service or firmware changes can still affect compatibility; see
  [tested coverage](status.md) and the [compatibility record](versioning.md).
- Windows remains a supported platform commitment with native CI and release
  artifacts, despite the limits above; see [platform coverage](status.md#platforms).
- **Deliberately kept features:** lazy sessions; pause threshold override;
  all-interface and integrated discovery; updater listeners and optional
  wide-video forwarding; flexible binds, remapped or disabled ports; multi-device
  selection and pagination; tunnel server role and loopback allowlist;
  historical/service layouts; internal IOTC port-count and RDT modes;
  injection seams; text output and log levels; diagnostic error graphs and
  vocabulary; relay progress snapshots, counters and summaries; discard
  telemetry and audit rule IDs; per-client lifecycle and throughput logs;
  wildcard exposure warnings; synthetic fixtures, reduced-capacity mode and
  metamorphic corpus; container packaging; macOS, Intel Mac and other CPU
  artifacts; Cobra/pflag and shell completion; optional pre-push hook. The
  maintainer chose to keep these capabilities and their supporting code.
