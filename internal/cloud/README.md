# Cloud/auth integration

Fixtures are independent synthetic transcriptions of the consumed DTO fields,
not live captures. See the [developer guide](../../docs/development/README.md#test-layout-and-evidence)
for origins and limitations.

`cloud` provides pure typed JSON decoding and a single-owner account machine.
`internal/cloud/cloudhttp` is the I/O edge for account requests, Detail/Acquire and lease
stop cleanup. Allocation is imperative; there is no separate pure preparation
machine. `NewFromEnvironment` accepts:

- `SEESTAR_EMAIL` and `SEESTAR_PASSWORD`: required environment-only email login
  on `https://api.seestar.com`. No UI whitespace removal or normalization is
  applied. The [recovered email regex and 8–32 UTF-16-unit password length](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#input-preparation) are
  checked locally; credentials are never command-line options.
- `Config.StateDir`: empty by default, generating a random UUID v4 held only in
  memory for this client/owner lifetime. No config-home lookup or file I/O.
  A nonempty path (CLI `--state-dir`) persists the
  [source identifier (`device_uuid`)](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#storage-startup-and-source-identifier)
  at the local path `InterProcessKV/device_uuid`, reused verbatim thereafter, mode 0600 inside a 0700 directory. File locking
  bounds concurrent creation/empty-file repair to one second. Unsafe modes,
  symlinks and oversized files fail closed with an attributed `--state-dir` error.

See [catalog selection mechanics](../../docs/reference.md#device-selection). Even a known refusal code retains a usable identity for correlation, independently of missing or malformed status and SSID. A refusal without a usable identity keeps its existing classification.

`Config.AccountBaseURL` overrides the account origin for testing; `BaseURL` is
still the separate remote origin. Both require HTTPS without user info, query,
fragment, or a path prefix. The default remote origin remains
`https://api-ithings.zwoastro.com`. The constructor requires account environment credentials. The CLI supplies
serial/model/storage flags through its configuration.

Login is lazy, before the first remote request, or explicit via `Client.Login`.
It performs [login and profile fetch](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#interactive-emailpassword-login); credentials and
tokens stay in memory and are never written to the app's mainKV or any other file.
Pure profile I/O failure leaves its installed token usable. Completed malformed,
denied or fatal account outcomes
retire credentials and token and block later authorization, including a completion
that arrives after its caller cancels. A canceled admitted operation with an
unresolved reply returns provisional uncertainty promptly, with an immutable
completion owned by the account worker. `cloudhttp.ResolveAccount` joins that
completion without sending a request. Startup discovery and eager acquisition
resolve it before choosing retry policy: purely retryable completion uses the
bounded jittered backoff; actual malformed, denied and fatal results remain
blocking. Attempt teardown joins independently of its canceled request context.
An interrupted startup waiter retains the unresolved handle and returns caller
cancellation. The owner still classifies the late response before admitting more work. Profile identity is not retained or
needed by online-link. Lease credentials and the returned Kalay license remain ephemeral.

Account `CheckProfile` requests implement bounded, serialized
[HTTP-401 credential replay](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#account-client-401-driven-credential-replay).
An ordinary profile 401 may be resolved by replay; an independent blocking
cause in the same response is retained and prevents replay. There is one cancellable account owner, no internal request queue and no worker
per waiting caller. Concurrent calls serialize and see the newest token. Failed
login or renewal with a blocking outcome is terminal until a new client is explicitly constructed: the
replacement deliberately never replays a stale token after failed credentials.
No time units or proactive refresh are invented for accessExpire/refreshAfter.

[Remote HTTP 401 is different](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#remote-client-token-expired-event): it reports `ErrReauthentication`, clears local
account credentials, and never automatically renews or replays online-link.
Obsolete responses cannot clear a different current token. HTTP 403 retains the
existing cloud authorization-failure policy and does not trigger renewal.
Stop uses the current account token for one independent cleanup attempt,
including cleanup after a refused allocation response. With no current token,
stop reports reauthentication required without sending a request. [Server
acceptance of stop with a different token remains unresolved.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop)

`AccountError` preserves HTTP status and any numeric envelope code separately.
Account codes are optional diagnostics: absent/null codes are accepted, and a
supplied code must be an integer. Profile HTTP 200/201 and empty HTTP 204 complete
successfully; nonempty bodies still pass bounded JSON validation. Interactive
login still requires HTTP 200 and a valid token; renewal accepts any 2xx with a
valid token. An interrupted exchange cannot establish successful completion.
No numeric bad-password, locked/disabled-account, region, challenge, or rate-limit
catalog is established. Such responses stay distinct by status/code without
invented labels or retries. Untrusted server messages are discarded because they
can echo secrets; every error and formatted secret wrapper is safe for logging.
These choices preserve the installed token only after pure profile I/O failure and
block authorization after completed malformed, denied or fatal outcomes.

`Client.Close` cancels and joins the account owner and clears its credentials.
It implements [local logout](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#explicit-logout). Close the device leases before closing the client. As before,
callers own and join remote operations. A transport must honor context deadlines.
Go memory reclamation is not a secure-erasure guarantee, and the caller's process
environment remains under its control.

The composed runtime uses `Detail` and `Acquire` through `proxy.allocator.Allocate`.
A returned `Lease` is owned by `sessionedge.attempt`, which closes transports,
joins workers and stops the lease before replacement. Partial allocation cleanup
stays with `Acquire` until ownership transfers. Duplicate lease closes share one
stop attempt. The session owner serializes attempts per device; the HTTP edge
performs no allocation retries of its own.

See [HTTP limits](../../docs/reference.md#protocol-resource-policies) and [JSON input policy](../../docs/reference.md#json-file-compatibility).

[Online-link returns the decoded identifier for cleanup](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link) even if other fields or
response classification fail. Invalid, truncated or oversized JSON yields no
cleanup identifier; the cloud API provides no lookup or idempotency contract to recover
an allocation whose response cannot be decoded. Cleanup has an independent deadline.

See [http work limits](../../docs/reference.md#protocol-resource-policies). Injected transports
must obey deadlines, single-exchange semantics and Request.Close, and must not log
request/response material. Each request requires connection closure, and the owned
transport also disables keep-alives, avoiding net/http's HTTP/1 idle diagnostics
which can otherwise print unsolicited secret-bearing response bytes.
Close the client after joining
its operations to release idle connections. The account path owns one goroutine;
net/http owns transport goroutines, closed
by response/context/client lifetime. Account admission plus operation uses the
configured timeout (default 30 seconds); renewal has a tighter 10-second bound.
Remote expiry uses a one-token pending flag and nonblocking, coalesced wakeups.
It survives request cancellation and is applied before subsequent account operations.

Request headers reproduce the [documented platform/channel/version](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#host-transport-and-headers) and use fixed
English/US locale metadata with empty model/OS values (no Android device identity
is fabricated). Compatibility of those descriptive fields remains unverified.
Cloud state machines emit no logs. The logger-aware catalog parser and HTTP edge
emit sanitized diagnostics.
Secret-bearing values redact formatting and structured slog output. Explicit secret accessors exist only for transport
handoff and must never be logged.

Received HTTP 401 and 403 remain authorization failures even when the body is
missing, truncated, oversized or canceled. Errors retain sanitized body failures
for `errors.Is`, and captured allocation identifiers still require cleanup.
HTTP 401 reports token expiry; treating 403 as an
authorization failure is local policy, without assuming vendor token expiry.

Documented refusal codes [300403 (service busy) and 300404 (occupied)](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#cloud-and-workflow-error-codes) return
ErrBusy without HTTP-edge retry, subject to the detail identity correlation above.
See [busy retry policy](../../docs/reference.md#availability-and-busy-devices).

See [cloud diagnostic records](../../docs/reference.md#logs).

Response validation follows program consumption:

| Response | Consumed JSON fields |
| --- | --- |
| Login / renewal | optional diagnostic `code`, required `data.accessToken` |
| Stop | optional diagnostic `code` |
| Device list | optional `code`; required item `deviceSn`, `deviceModel`, `deviceStatus` and `ssid`; optional `data.total` and `data.current` |
| Device detail | optional `code`; required `data.deviceSn`, `data.deviceModel`, `data.deviceStatus` and `data.ssid`; LAN discovery also retains `ssid` |
| Online-link | `code`, `data.conId`, `data.deviceUid`, `data.sdkLicenseKey`, `data.username`, `data.password` |

Codes, statuses, totals and current-page hints use Go `int`. Missing/null,
negative or understated totals are treated as unspecified (-1); negative and
understated hints are ignored and logged at debug. A positive current-page hint
is used; other values fall back to the requested page. Catalog status and ssid
must have the required presence and types, then are discarded. Detail status
controls availability; detail ssid is retained only for LAN discovery. Identifiers
`conId` and `deviceSn` are strings. Tokens, models, UID, license and tunnel
credentials retain their string checks. Other unused metadata is ignored.
