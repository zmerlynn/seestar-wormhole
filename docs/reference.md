# Command reference

Use this page when you script seestar-wormhole or need an exact option or
failure rule. For setup examples, see [phone app](phone-app.md),
[always-on hosts](always-on.md), [pulling images](pulling.md), and
[other clients](clients.md). The [protocol guide](protocol.md) connects the
proxy's wire behavior and local policy to the v1 specification.

- [proxy](#proxy): listeners and flags
- [pull](#pull): flags, selection, receipts, and output
- [licenses](#licenses)
- [Global command behavior](#global-command-behavior) and [exit codes](#exit-codes)
- [Availability and busy devices](#availability-and-busy-devices)
- [Site protection](#site-protection)
- [Device selection](#device-selection) and [installation identity](#installation-identity)
- [Logs](#logs), [traces and capture keys](#traces-and-capture-keys)
- [Containers](#containers), [Windows](#windows), [LAN discovery](#lan-discovery)

## proxy

```sh
seestar-wormhole proxy
```

The app cannot set the device's site, clock or time zone through the proxy.
See [site protection](#site-protection). The command takes no positional arguments. It forwards TCP through one shared
remote session. Clients can issue device commands and read files; there is no
local authentication or read-only command whitelist. Local connections are
plaintext. This is not an ALPACA server. Keep listeners on
loopback or a trusted network behind a firewall.

### Proxy flags

| Flag | Default | Meaning |
|---|---|---|
| `--bind` | `127.0.0.2` | TCP bind address, resolved by the OS. Discovery requires a literal LAN IPv4 address or `0.0.0.0`. |
| `--port local:device`, `-p` | See mappings below | Repeat to replace one device port's listener or add a device port. Local `0` disables that device listener. |
| `--idle` | `0` | Nonnegative Go duration. Zero connects eagerly and holds the session. Positive values connect on demand and release after the last client has been absent this long. |
| `--pause-threshold` | `4194304` | Per-channel receive-pressure high water in bytes, from `2` through `8388608`. See [flow control](#flow-control). |
| `--stats-interval` | `1s` | Nonnegative Go duration. Zero disables collection and periodic telemetry work. Stats records require debug logging. |
| `--lan-discovery` | `false` | Answer UDP 4720 scans on selected LAN interfaces. Unsupported on Windows. |

Proxy also accepts every [shared flag](#shared-flags).

### Port mappings

See [the device port map](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/ports.md#device-ports), [the app tunnel mappings](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/ports.md#remote-tunnel-port-mapping) for the wire rules.

| Local TCP port | Device TCP port | Service |
|---|---|---|
| 4700 | 4700 | JSON-RPC control |
| 4445 | 445 | SMB |
| 8080 | 80 | HTTP |
| 4350, 4554, 4555, 4801, 4800, 4804, 4361 | Same port | Other device services, including imaging on 4800 |

All ten default local ports are unprivileged. `-p 1445:445 -p 80:80` replaces
only SMB and HTTP; other defaults remain. `-p 0:445` disables SMB.
A device port outside the default set adds a listener.

Device ports must be `1..65535`; local ports must be `0..65535`.
Explicit device ports and nonzero local ports must be unique. Final nonzero
local ports must also be unique across defaults and overrides. You can supply
at most 128 overrides and retain at most 128 final mappings.

Phone album/image URLs use HTTP without a port, so you need `-p 80:80` for
those views. Discovery without that mapping logs `lan_http_port` and continues.
An OS bind failure, including a used or privileged port, stops startup. The
proxy provides no privileged helper or launchd socket activation.

### Flow control

The proxy applies the [PAUSE/RESUME thresholds](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#pause-resume-and-wake)
to each channel's retained bytes plus its proportional share of shared RDT
receive bytes; empty channels receive no shared charge. This local accounting
is not negotiated peer credit. Pending controls coalesce when RDT is blocked.

Receive ceilings are 16 MiB per channel and 64 MiB across channels, allocated
on demand. The session supports at most 128 pending/active clients, with at
most 16 queued during acquisition. Its application buffer reservation is
154 MiB within a 160 MiB policy ceiling; that is not an RSS limit.
Slow consumers remain subject to progress-aware reclamation. These budgets,
timeouts, and retry policies have no CLI overrides beyond the flags above.

Bulk data and RPC replies share the device's ordered send path. Local flow
control cannot move a reply ahead of [bytes the device has already queued](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause).
See [pulling images](pulling.md) for transfer tuning and measured rates.

## pull

```sh
seestar-wormhole pull --via 127.0.0.2:4445 --since 24h --verify ./MyWorks
seestar-wormhole pull --busy-wait 90s --timeout 30m --verify ./MyWorks
```

Supply exactly one local destination. The destination contains paths relative
to `EMMC Images/MyWorks` when `--dir` is omitted; pull adds no night or
`MyWorks` directory. With one or more `--dir PATH` flags, each selected directory
lands in `DEST/PATH`. `--dir MyWorks` therefore adds `DEST/MyWorks`.

For images and dark sequences together, use a separate archive destination:

```sh
seestar-wormhole pull --via 127.0.0.2:4445 --dir MyWorks --dir Autorun/Dark --dir darklibrary ./archive
```

That produces `./archive/MyWorks/...`, `./archive/Autorun/Dark/...`, and
`./archive/darklibrary/...`.
Paths are relative to the `EMMC Images` share, with forward slashes. Absolute
paths, `..`, empty segments, backslashes, reserved names, and names outside the
portable path rules below are refused. Duplicates, overlapping directories
(such as `Autorun` and `Autorun/Dark`), and case/Unicode collisions are refused
before connecting. A missing selected directory fails with `missing_directory`,
even if `--only` would select nothing.
It embeds rclone's SMB/local operations. You need neither a separate rclone
installation nor an SMB mount. [It uses guest with an empty password.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#protocol-and-access)
It never deletes remote files or prunes unrelated local files.

Without `--via`, pull uses the account credentials, owns one device session,
and releases it. It opens SMB only. With `--via`, it
uses an existing proxy's SMB listener and never acquires or releases a session
or falls back to direct mode. That pull process needs no cloud credentials.

### Pull flags

| Flag | Default | Meaning |
|---|---|---|
| `--via` | Empty; direct mode | SMB `host:port` of an existing proxy. Use brackets for IPv6, such as `[::1]:4445`. Port must be numeric, `1..65535`. |
| `--dir` | MyWorks contents directly in DEST | Repeatable share-relative directory under `EMMC Images`; each selected PATH lands in `DEST/PATH`, including MyWorks. |
| `--since` | `auto` | Inclusive SMB mtime lower bound: `auto`, `all`, zoned RFC3339, or positive Go duration ago. |
| `--until` | Empty | Inclusive upper mtime bound: zoned RFC3339 or positive duration ago. Capped by run start minus settle. Explicit empty is invalid. |
| `--settle` | `2m` | Nonnegative duration. Defer files newer than run start minus this value. |
| `--overlap` | `5m` | Nonnegative lookback from an auto checkpoint. |
| `--only` | No filters | Repeatable, case-sensitive Go regexp (RE2) union over immediate child folder names of every selected directory (MyWorks by default). Quote each expression. |
| `--verify` | `false` | Reread new copies remotely and compare bytes; upgrade size receipts by comparison. |
| `--recheck` | `false` | Force download comparison of eligible files, including matching reread receipts. Also reread new/replacement copies. |
| `--sums` | `true` | Atomically refresh `SHA256SUMS` on success. False suppresses only the manifest. |
| `--transfers` | `4` | Concurrent copy/verification workers, `1..8`. Each worker copies and checks sequentially. |
| `--bwlimit` | `0` | Unlimited, or a finite positive rate with explicit `bytes/s`, `KiB/s`, or `MiB/s` suffix. Examples: `500000bytes/s`, `512KiB/s`, `1.5MiB/s`. |
| `--busy-wait` | `0` | Nonnegative initial-acquisition/occupancy budget. Zero fails fast on recognized busy. Direct mode only. |
| `--timeout` | `0` | Nonnegative total bootstrap/work timeout. Zero is unlimited. Cleanup has separate bounds. |
| `--rebind` | `false` | Deliberately take over the destination's source binding and reset checkpoints. |
| `--dry-run` | `false` | Inventory and plan without local writes or source content reads. |
| `--error-on-empty` | `false` | Exit 4 for `nothing-new`; deferred and planned results still exit 0. |
| `--output` | `text` | `text` or one final `json` summary object on stdout. |
| `--progress` | `false` | Bounded aggregate file/byte/rate progress on stderr. |

Pull also accepts every [shared flag](#shared-flags), with these restrictions:

- `--via` rejects explicitly supplied `--serial`, `--model`, `--state-dir`,
  `--trace`, `--keylog`, and `--busy-wait`, even with empty/default values.
- `--dry-run` rejects explicitly supplied `--state-dir`, `--trace`, `--keylog`,
  and `--rebind`, even with empty/default values.

Proxy-only flags are unavailable to pull. Standalone rclone flags such as
`--contimeout`, `--retries`, `--buffer-size`, and `--multi-thread-cutoff` are
unavailable too. Pull disables multistream splitting and allows up to three
low-level attempts per operation within the work timeout. Copy and comparison,
including local verification reads, share the aggregate bandwidth budget.

### Selection and time windows

One UTC run-start instant defines duration-ago bounds. `24h` is elapsed time,
not an observing night; there is no `d` unit. Fractional RFC3339 seconds are
accepted. Bounds are inclusive. The upper bound is the earlier of `--until`
and run start minus settle. Files at that upper bound are eligible.

`--only 'M 31'` selects `M 31` and `M 31_sub`; `--only '^M 31$'` selects the
exact folder. Escape literal parentheses, for example `"M 31 \(wide\)"`.
Any matching expression selects the folder and all descendants. An unmatched
union selects nothing. No filters selects all folders and root files; filters
exclude files directly inside each selected directory. For `--dir Autorun/Dark`,
`--only` matches folders immediately inside `Autorun/Dark`, not `Autorun` or
`Dark` itself. All `--only` expressions form one global union applied to every
listed directory, regardless of flag order.
These are regexps, not globs. Invalid regexps exit 2 with
the expression's 1-based index.

Each directory and sorted, deduplicated filter set has its own auto checkpoint.
Adding or removing `--dir` flags cannot lend another directory its checkpoint;
a newly selected directory starts with a full settled scan. Every run inventories folders;
missing files, changed source size/mtime, wrong local size, and missing/stale
receipt provenance bypass auto's lower bound. So do size receipts needing a
`--verify` upgrade. Settle always applies. Explicit lower bounds are respected.

A successful auto run advances its checkpoint to the upper bound only after
all eligible work and owned-session release succeed. A historical window
never moves a checkpoint backward. Failed/interrupted runs keep the old
checkpoint. Explicit `--since all` or time/age runs save receipts without
advancing auto checkpoints. For a fixed observing window, reuse one zoned lower
bound between scheduled passes; a moving `24h` shifts it each time.

Invalid or future source mtimes fail the run without a checkpoint update.
Auto cannot discover every same-size/same-mtime change or clock rollback.
Use `--since all --recheck` periodically to compare the whole settled archive.

### Copies, receipts, and verification

A new copy reads the source once by default. Pull checks the actual temporary
file length and source byte count against the listed size, and checks stable
source size/mtime before and after copying. A successful copy gets a
`check: size` receipt. This detects length errors, not same-size corruption.

`--verify` adds a complete remote reread and byte comparison before promotion.
Its receipt says `check: reread`. The device publishes no independent checksums;
this checks transport against the same source. The source can still change
after the final metadata check; pull does not take a device snapshot.

Either receipt skips source content reads on default repeats when source
binding/generation, source size/mtime, and local existence/size match. Local
mtime does not need to match. Default repeats do not rehash local contents.
With `--verify`, a size receipt needs comparison and can become a reread receipt
without recopying equal bytes. A matching reread receipt already satisfies
`--verify`. `--recheck` compares even those receipts within the selected window.

An equal-size local file without a matching receipt must pass download
comparison before reuse or attribution. Changed/corrupt files use a sibling
temporary file and replace the old file only after checks succeed. Incomplete
files restart from byte zero. Completed receipts are saved as files finish
and survive interruption. Reusing receipts therefore makes repeated bounded
pulls practical; a timeout does not restart completed work as a batch.

`SHA256SUMS` hashes the local bytes. It uses two spaces and relative `/` paths,
compatible with `sha256sum -c SHA256SUMS`. It covers the saved receipt inventory,
including prior entries, rather than every unrelated local file. Receipts can
be newer than the manifest after interruption. `--sums=false` leaves copy
checks and requested comparisons active.

### Destination state and safe paths

State is `<dest>/.seestar-wormhole/state.json`: schema version 4 for the default
layout, version 5 for explicit directories. Default-layout state and receipt
serialization stay unchanged. Older binaries refuse explicit-directory state. Receipts store
path, size, UTC remote mtime, SHA-256, check kind, source binding, and generation.
Corrupt or newer-schema state fails closed; restore or move it explicitly.
State is limited to 64 MiB. Remote and local inventories each have a
200,000-entry limit, shared across all selected directories.

| Mode | Source binding |
|---|---|
| Direct, without explicit `--model` | `direct:<sha256(serial)>` |
| Direct, with explicit `--model` | `direct:pair:<sha256(serial + NUL + selected catalog model)>` |
| Via | `via:<normalized host:port>` |

Via normalization lowercases hostnames, removes a trailing hostname dot,
canonicalizes IP literals and numeric ports, and does not resolve DNS.
This binds an address, not an authenticated telescope identity.
Switching modes, endpoints, or adding `--model` requires another destination
or deliberate `--rebind`, even for the same telescope. Mismatch exits 2 and
reports only existing/requested source kinds.

The default flattened layout and the explicit-directory layout are kept in
separate destinations. Once state binds a destination to either layout, switching
fails with `layout_mismatch` (exit 2), including dry runs and `--rebind`. An
explicit-directory run saves its binding after every requested directory passes
inventory, path checks, and planning, before any data or receipt write. Failures
before that point leave saved state unchanged; later failures reserve the layout.
Changing the directory list within explicit mode is allowed; receipts and
checkpoints from earlier selections remain. `--rebind` changes the source,
not the layout. Destinations with no saved state have no recorded layout;
the example archive setup starts with an empty directory.

`--rebind` saves a fresh generation and clears all selection checkpoints. Default
mode saves immediately, even if later work fails. Explicit-directory mode stages
those changes until the inventory and planning point above; earlier failures
retain the saved source, generation, and checkpoints. It retains receipts with
their old provenance until checks under the new source succeed. Size alone cannot
import those bytes. Version 3 receipts migrate as reread receipts with their
provenance; versions 1/2 need provenance repair. Version 1 glob checkpoints
are retired. Older readers refuse version 4.

An OS destination lock refuses overlapping writers before cloud login and is
released on process exit. Do not remove its persistent lock file to bypass it.
Dry-run reads existing state without the lock, directory creation, partial
cleanup, manifest, receipt, or checkpoint writes. Its plan is advisory if another
process changes the destination. Direct dry-run still opens/releases a session.

Portable path checks apply on every OS: containment, case/Unicode collisions,
symlinks/reparse points, and nonregular output paths. Names cannot contain
backslashes, Windows-reserved punctuation, control characters, dot traversal,
trailing dots/spaces, or Windows device names. Pull reserves the root names
`.seestar-wormhole` and `SHA256SUMS` and its temporary-file name patterns.
It refuses unsafe names rather than renaming them.

Data, directories, and the manifest respect your umask or inherited Windows
data ACL. Use `umask 022` for shared ingest. Metadata stays private. On Unix, create metadata directories/files
with `0700`/`0600`; existing metadata must have no group/other permissions.
Pull refuses unsafe permissions and never repairs them automatically.
Installation UUID storage is separate from destination state.

### Busy, timeout, and cancellation

Direct busy detection uses device detail or recognized vendor busy codes in a
complete HTTP 400 allocation response. Generic TCP/SMB failures are not proof
of busy. With `--via`, a down endpoint or timeout exits 1; inspect proxy logs
for cloud occupancy. A lazy proxy dial is covered by pull's `--timeout`.

`--busy-wait` gives initial acquisition, backend connection retries, and
recognized occupancy one deadline from the first acquisition attempt. Retries
do not renew it. Expiry before acquisition exits 3. Once acquired, transfers
and ordinary transport recovery can outlive it. A successful recovery allocation
proceeds; a recognized busy response after the deadline exits 3. Without this
flag, recognized busy fails fast during recovery too.

A positive `--timeout` covers bootstrap and work. SMB shutdown then has a
separate five-second bound, followed by session release's five-second cloud
stop bound and worker cleanup. Ctrl+C exits 130 after cleanup. Failed release
overrides success and busy with exit 1. A forced kill cannot guarantee release.

### Summary and progress

Text stdout ends with `status`, copied/verified/deferred file counts, copied
bytes, `first_scan`, inclusive UTC bounds, `reason`, and `cleanup_reason`.
Planned text also includes `planned`. `verified` counts checks this run,
including size checks; it does not mean every counted file was reread.
See [status and reason values](#status-and-reason-values) for the vocabulary
and exit codes. A successful check-only run can be `nothing-new`.

`--output json` writes exactly one object plus newline on stdout. Logs and
errors stay on stderr. Field order is not a contract; tolerate added fields.

| Fields | Type and meaning |
|---|---|
| `status` | Classified string from the [vocabulary table](#status-and-reason-values) |
| `copied`, `verified`, `deferred` | Integer file counts |
| `planned` | Integer files needing work in dry-run; zero otherwise |
| `bytes` | Integer copied bytes, or listed planned source bytes in dry-run |
| `first_scan` | Boolean; at least one selected directory/filter pair has no saved checkpoint entry |
| `lower`, `upper` | Inclusive UTC RFC3339 timestamps; earliest per-directory lower bound and common upper bound |
| `reason`, `cleanup_reason` | Classified strings; `none` when absent. |
| `exit_code` | Final integer process exit code |

Zero lower (`0001-01-01T00:00:00Z`) means unbounded. Both bounds zero means
failure before window resolution. Runtime failures include summaries after
cleanup, including source mismatch, busy/locked, timeout, and interruption.
Parse/option-validation errors have no summary. Neither do help/version.
Forced termination or broken stdout can leave output incomplete. A summary
write failure exits 1, so do not trust a partially written `exit_code`.

Dry-run succeeds with `status=planned`, `copied=0`, `verified=0`, and planned
file/byte totals, including an empty or deferred-only plan. `--error-on-empty`
does not change that. Debug stderr shows each file's `copy`, `check`, `skip`,
or `defer` action and listed bytes. `check` means an equal-size existing file
needs comparison; listing cannot decide reuse versus replacement. Planned
bytes exclude skipped/deferred files and extra comparison reads.

Counts, bytes, receipts, manifests and progress cover every selected directory.
Progress shows numeric aggregates only. Files done/total exclude skipped
receipts and deferred files. Bytes include copy traffic, comparison reads from
both sides, and retries; native backend hashing is not counted. Total bytes
grow as comparison work is found and differ from final copied bytes. Inventory
and dry-run show zero content work. A terminal gets one live line at most once
per second. Redirected stderr gets a line every 30 seconds, capped at 120 lines
and 30 KiB per run. The first update is queued immediately. A blocked progress
sink does not delay cleanup or stdout indefinitely: final drain allows one
second, and clearing the live line is best effort.

### Standalone SMB clients

You can also use standalone rclone against the proxy. Configure its host and
local SMB port explicitly; [`EMMC Images` is the share](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#shares) and `MyWorks` is the
default pull directory:

```sh
rclone --config seestar-rclone.conf config create seestar smb host 127.0.0.2 port 4445 user guest
rclone --config seestar-rclone.conf lsf "seestar:EMMC Images/MyWorks" --recursive --files-only
rclone --config seestar-rclone.conf copy "seestar:EMMC Images/MyWorks" ./MyWorks
rclone --config seestar-rclone.conf check --download "seestar:EMMC Images/MyWorks" ./MyWorks
```

Use your actual bind address/port. Guest has an empty password. Download
comparison reads both sides; copy alone does not establish byte equality.
Standalone rclone does not use pull's receipts, checkpoints, or destination lock.

## licenses

```sh
seestar-wormhole licenses
```

Prints the project NOTICE, then module paths, SPDX identifiers, and full
license/NOTICE texts for third-party libraries linked into this binary.
It includes `internal/edge/PION-LICENSE` and the LGPL-3.0/GPL-3.0 texts for sddl.
It needs no credentials, network, or source checkout. It takes no flags except
`--help`/`-h` and no positional arguments. Usage errors exit 2; output failure
exits 1. See [NOTICE](../NOTICE) for attribution and relinking instructions.

## Global command behavior

Bare `seestar-wormhole` prints root help and exits 0. Use `help [command]` or
`--help`/`-h` for help. Root `--version` prints
`seestar-wormhole <version> (<revision>, <os>/<arch>)`; unset build metadata is
`development`. Help/version require no credentials or network. Version still
validates other supplied flags, and pull version can omit the destination.
See the [versioning policy](versioning.md) for the release contract.

Operational flags are local to `proxy` and `pull`; they are not root options.
Long flags use double dashes. The only operational short flag is `-p` for
proxy ports. Boolean flags need no value to enable; use `--flag=false` to
disable. Scalar flags use the last supplied value; repeatable ports/filters
accumulate. Durations use Go units such as `2m` and `1h30m`, without a `d` unit.

### Shared flags

These are available separately on proxy and pull, subject to pull's mode rules.

| Flag | Default | Meaning |
|---|---|---|
| `--serial` | Empty | Select an account device serial; required for multiple distinct catalog pairs. |
| `--model` | Empty | Exact catalog model when a serial is ambiguous or completeness is unproven. |
| `--state-dir` | Empty; memory only | Persist installation UUID at `<path>/InterProcessKV/device_uuid`. Explicit empty selects memory-only operation. |
| `--log-format` | `json` | `json` or `text` stderr records, UTC millisecond timestamps. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. |
| `--trace` | Empty; off | New private redacted JSON Lines trace. Creation failure stops startup. |
| `--keylog` | Empty; off | New private sensitive capture-key file. Creation failure warns and disables capture. |
| `--version` | `false` | Print version/revision/platform and exit after validation. Also available at root. |

The only application-specific runtime environment variables are
`SEESTAR_EMAIL` and `SEESTAR_PASSWORD`. They are required for proxy and direct
pull and stay in memory. No credentials are accepted in argv or persisted.
There is no config-file loader. Serial/model
inputs must be nonblank, at most 4096 bytes, without NUL, CR, or LF. Email must
be valid UTF-8, at most 4096 bytes, and match
`^\w+([-+.]\w+)*@\w+([-.]\w+)*\.\w+([-.]\w+)*$`.
Password must be valid UTF-8 and 8–32 UTF-16 units.

Flag parsing and pure validation precede runtime files, credentials, and
listeners. Configuration errors emit one JSON error with fixed `option`,
`rule`, and command-help hint, regardless of requested log settings. Supplied
values, paths, coordinates, and unknown option names are omitted. Unknown
flags/positional arguments use `option=arguments`. Startup bind, trace-creation,
and identity-storage errors identify `--bind`, `--trace`, and `--state-dir`.
Keylog warnings identify `--keylog` and remain best effort.

### Exit codes

See [status and reason values](#status-and-reason-values) for the complete
mapping. For scheduled pulls, retry transient failures after a pause, inspect
`reason` and `cleanup_reason`, and avoid automatic `--rebind`.
Neither nothing-new nor one clean pass proves an observing night is finished.

## Availability and busy devices

The [cloud may report remote allocation as busy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#observed-single-session-enforcement).
Phone LAN use through the proxy shares its session. Before switching to the
app's remote mode, stop the proxy and wait for exit to hand ownership back;
persisted identity does not bypass cloud occupancy.

With default `--idle 0`, proxy connects at startup and retries recognized busy
indefinitely, including the first dial. Equal-jitter delays start at 0.5–1
second and grow exponentially to 15–30 seconds. After a ready session loses
transport, recovery is automatic. No new client or restart is required.
A nonbusy failure on the first eager dial is fatal.

With positive `--idle`, startup can still authorize/read the catalog and,
for discovery, detail. Session allocation waits for a client. Client-triggered
busy retries hold waiting clients for at most 90 seconds from the first dial;
later clients share that deadline. Delays start at 2–4 seconds and double their
cap to 15 seconds. Expiry closes waiting clients while the proxy stays up.
New demand gets a fresh budget, never bypassing the 0.5-second minimum dial
interval. When all waiting demand cancels, retries stop. Authentication,
identity, and protocol errors do not enter this busy policy.

Cleanup has to join before replacement. The client deadline does not extend
for first-response latency or cleanup, and closing waiting clients does not
abandon an allocated session's release. Uncertain ownership and cleanup
failures block replacement, except for bounded cloud-stop abandonment during
transport recovery; see [session ownership policy](../DESIGN.md#session-ownership-implementation).

| State | What local clients experience |
|---|---|
| `dormant` | Lazy mode waits for demand. |
| `dialing` | Initial acquisition/lazy clients wait. Eager busy recovery and recovery after ready accept then close clients. |
| `ready` | Forward to the device. |
| `releasing` | Close clients and join bounded cleanup; retryable lazy opens can remain queued within their deadline. |
| `backoff` | Eager retries automatically and closes clients; lazy demand waits for the next allowed dial. |
| `busy` | Fresh lazy demand can retry cloud refusal; uncertainty/failed release remains blocked. |
| `closed` | Listeners and session cleanup have finished. |

State logs describe `local_clients=wait`, `forward`, or `accept_close`.
Repeated down-state connections do not each produce rejection warnings.
TCP clients receive closure, without diagnostic text inserted into their stream.

## Site protection

The app cannot set the device's site, clock or time zone through the proxy.
Protection is unconditional and has no flag. The device keeps its stored site
and maintains its own clock. To change the site on purpose, connect the app
to the device without the proxy.

The proxy sends no requests of its own at startup and does not read the site.
Client forwarding follows normal session admission without a site-read gate.

Protection inspects client-to-device CRLF JSON requests on device ports
[**4400 and 4700–4702**](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/ports.md#framing-per-port-family), including remapped local listeners. Port 4400 is not
enabled by default. Other ports, including 4350, 4800, 4801, and 4804, stay
byte-transparent. Device-to-client bytes stay unchanged.

See [the site and host-clock setters](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#user-location), [the time setter](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#setting-the-time-pi_set_time), [mount time](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#mount-time), [set_setting](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/device-state-and-settings.md#writing-set_setting) for the wire rules.

| Method | Behavior |
|---|---|
| `set_user_location`, `pi_set_time`, `scope_set_location`, `scope_set_time` | Drop the whole request and answer locally with JSON-RPC `-32602`, message/reason `site_protected`, regardless of params. |
| `set_setting` | Pass unchanged. |

Unsafe frames are dropped and answered locally. These error objects differ from
the [device error envelope](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/errors.md#errors-on-the-wire):

| Input | JSON-RPC code |
|---|---|
| Malformed JSON, duplicate member names anywhere, JSON batch arrays, null/nonobject envelopes | `-32700` |
| Missing method, wrong nonnull method type, control-character method, non-CRLF frame, incomplete EOF fragment, frame over 16 KiB including CRLF | `-32600` |
| Protected setter | `-32602` |

Empty-string and null methods pass through unchanged because they match no
protected setter. Whitespace-only separators also pass through. Oversized
frames are discarded through the terminator without forwarding a prefix.
Errors echo a recoverable string or numeric ID; otherwise ID is null. Local
errors wait for a device frame boundary.

Inspected requests with duplicate member names are rejected before forwarding,
including nested names and escaped spellings. This is a deliberate
fail-closed deviation from device dispatch; [R12 prohibits clients from sending
duplicate members](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake.md#request-envelope).
Unknown members remain allowed. The local answer uses `-32700`, a recoverable
string or numeric ID (null when unavailable or ambiguous), and message
`request rejected by site protection`; recovery inspects at most the first 16 KiB.
Device replies stay byte-transparent and are never decoded for this check.

One WARN `site_protected` record is emitted for each dropped setter. It contains
`method`, integer request `id` or `unavailable`, and `reason=site_protected`.
String IDs are echoed to the client but omitted from logs. Coordinates, clock
values, zone values, and unknown parameter names are omitted. Invalid frames
use WARN `site_reject` with fixed method/reason labels. There is no startup
site record. If device EOF prevents a local error from being delivered, WARN
`site_reply_failed` names the refusal rule in `reason` with `cause=device_closed`.

## Protocol resource policies

The [protocol interaction guide](protocol.md#local-policy-where-the-spec-is-silent-or-unresolved)
explains the spec boundary for each choice. These are local limits, not server
guarantees. The catalog, identity, availability and discovery sections give
their command details.

| Work | Limits and mechanics |
|---|---|
| Cloud HTTP | 30-second request deadline, 64 KiB bodies, 16 KiB headers, two connections per host; no redirects or automatic transport retries. TLS certificate verification remains enabled. |
| License parsing | 16 KiB JSON cap. Standalone-name selection has the ordering incompatibility described in [deviations](protocol.md#deliberate-deviations). |
| IOTC acquisition | 60-second total deadline, monotonic selector gates, direct preference within one selector interval, bounded endpoint/candidate tombstones. |
| Hello correlation | At least the 24-byte serial/instance form, tolerating trailing bytes; random serial origin and retained echo history across the attempt. |
| Attempt identity | Fresh cryptographic material and nonzero random RID per attempt; process-lifetime RID reservation with a 16,777,216-entry ceiling. |
| DTLS | Pion flight retries, 30-second handshake deadline, lifetime caps of 256 KiB and 512 control-bearing datagrams. |
| RDT receive | At most 8 MiB and 8192 packets, reserve the missing head before ACK; excess is not acknowledged. |
| RDT send | Numeric initial-send window, immediate accepted-tail flush, urgent retries spaced by at least 20 ms; no native blocking-write adjustment. |
| RDT ACK and RTT | 100 ms fallback from pending work; retransmission history survives transmitted-count wrap. |
| RDT close | Five-second bound for drain and FIN retries; standalone QUIT or a queued gap fails; no FIN_ACK linger. |
| Tunnel verification | One pending nonzero ID per open, five-second verification deadline; eight pending opens, retry about once per second, 35-second admission bound. |
| Tunnel flow | Apportion shared RDT backlog among nonempty channels, coalesce unsent controls, alternate whole controls with FIFO frames. |
| Tunnel receive storage | Default 4 MiB high water; 16 MiB per-channel and 64 MiB aggregate ceilings. Reclaim holders stalled for one second before larger progressing holders. |
| Composed session | 120-second acquisition bound; 128 pending/active clients, including 16 waiting during acquisition; 154 MiB application reservations within a 160 MiB ceiling. |

## Device selection

Selection uses distinct `(deviceSn, deviceModel)` pairs from the account
catalog. Without a serial, exactly one pair must remain. A model alone does
not disambiguate several pairs. Exact duplicate pairs retain the first record.
List status does not determine availability; detail is authoritative.

A serial shared by models requires an exact `--serial`/`--model` pair. With
one matching entry in a complete catalog, its catalog model wins even if you
supply `--model`. Unmatched serials or multiple pairs without a serial identify
`--serial` in diagnostics without exposing catalog labels or serials.

The proxy requests 100 items per page (local policy), at most 16 pages and
256 distinct pairs, with one 30-second per-catalog-operation timeout,
following the
[paging and completion guidance](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#device-selection) without a past-the-end request.

Completeness requires a positive total exactly equal to the received count.
Missing, null, zero, understated totals, page-limit exhaustion, or any exact
pair duplicate require `--model`. Select a received exact pair with serial and
model when completeness cannot be proven. Detail must then match both identity
fields before allocation; mismatch stops the dial. Selection itself makes no
device-session allocation. INFO reports only a public model label and masked
serial; unknown model labels are redacted. Duplicate counts appear at DEBUG.

## Installation identity

Without `--state-dir`, a fresh random UUID exists only in memory. Startup does
not consult a user config directory or identity file. Idle release/redial
reuses that process's UUID. `--state-dir` persists only the
[source identifier (`device_uuid`)](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#storage-startup-and-source-identifier)
at the local path `InterProcessKV/device_uuid`, never credentials. It can help frequent restarts;
it cannot eliminate a busy precheck or guarantee a vendor handover time.

On Unix the identity directory must have no group/other access and the file
must be regular with mode exactly `0600`; new directories use `0700`.
The identity file refuses symlinks. First creation/empty-file repair is locked
with a one-second acquisition bound. Existing nonempty valid identifiers are
retained. On Windows, protected owner/SYSTEM ACLs and reparse checks apply.
Supply trusted parent directories.

## Logs

Help, version, license text, and pull summaries go to stdout. Logs/errors go
to stderr. Normal logging uses the selected format and level. Default INFO
has no periodic stats. Use WARN for quieter long-running service logs.
Rejected startup discards pending dependency records.
Most diagnostic records contain fixed classifications and numeric context,
not dependency error text, forwarded payloads, or session secrets. Device
selection masks serials. Dry-run debug plans intentionally include filenames;
`wildcard_bind_warning` intentionally includes interface/address exposure.

| Record | Level | Meaning and useful fields |
|---|---|---|
| `proxy_started` | INFO | Revision, listener count, lazy flag, pause threshold; process serving, not necessarily remote ready |
| `installation_id_ephemeral`, `device_selected` | INFO | Identity storage mode; selected public model and masked serial |
| `session_state`, `session_phase_complete`, `session_release_complete` | INFO | State in `phase`, layer, reason, retry milliseconds, local-client policy |
| `lazy_open_attempt` | INFO | New client-triggered attempt and fixed cause |
| `layer_failed` | INFO for handled busy; ERROR for other failures | Classified phase/reason |
| `session_unavailable` | WARN | Outage reason, `unavailable_ms`, `busy_answers`, every five minutes during eager busy or recovery after ready |
| `cloud_retry` | WARN | Catalog transport retry and delay |
| `release_not_attempted` | WARN | Release needs reauthentication |
| `dtls_ready` | INFO | Negotiated version and suite |
| `iotc_rid_history_exhausted` | ERROR | Process-lifetime rendezvous identifier reservation budget is full; fixed `error` includes restart guidance |
| `relay_progress`, `relay_summary`, `relay_failure_summary` | INFO | Relay phase, request/result/refusal counters, selection; fixed failure reason in the failure summary |
| `channel_open` | INFO | Device `port`, `local_port`, `channel`, `client_class` |
| `channel_close` | INFO | Same context, `duration_ms`, successful `local_to_device_bytes`, `device_to_local_bytes`, classified reason |
| `channel_closed` | INFO for a closed local writer; WARN for abnormal failures; DEBUG otherwise | Tunnel/bridge detail, direction or channel, reason and classified cause |
| `client_rejected`, `listener_accept_failed` | WARN | Capacity/dial or accept failure, ports and classified reason |
| `client_canceled` | INFO | Admission canceled before forwarding; ports/client class |
| `site_protected` | WARN | Dropped setter; method, integer ID or `unavailable`, reason |
| `site_reject` | WARN | Invalid frame; method and reason |
| `site_reply_failed` | WARN | Local refusal could not be delivered; method, refusal reason, and cause |
| `lan_discovery_started` | INFO | UDP port |
| `lan_discovery_synthesized`, `lan_http_port` | WARN | Synthesized field names; missing HTTP 80 mapping |
| `wildcard_bind_warning` | WARN | Exposed non-LAN interfaces/addresses and TCP mappings; advisory snapshot, excluding discovery UDP |
| `key_logging_enabled`, `keylog_failed` | INFO / WARN | Sensitive capture enabled; dropped-line count or creation warning |
| `proxy_cleanup_complete` | INFO | Classified cleanup result |
| `proxy failed` | ERROR, always JSON | One final classified failure, after cleanup; `reason` and `cleanup_reason` are stable keys |
| `pull failed`, `licenses failed` | ERROR | Final/configuration diagnostics; option/rule or phase/reason/cleanup details |
| `phase_transition`, `rendezvous_path` | DEBUG | Layer transitions; direct/relay and UDP/TCP path |
| `cloud_catalog_pagination_oddities`, `cloud_receive_oddity`, `cloud_response_rejected` | DEBUG | Bounded cloud parsing/catalog diagnostics |
| `diagnostic_causes` | DEBUG | Bounded classified error graph correlated by diagnostic, phase, reason |
| `pull plan` | DEBUG, dry-run | Filename, action, listed bytes |

`cloud_license_missing` means [the cloud allocation omitted `sdkLicenseKey`](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link) or
returned it null or blank. Setup fails in the cloud phase, releases any valid
allocation identifier, and blocks automatic redial. It is a service-side change;
local configuration cannot supply the missing key. Error records include a fixed
`hint` explaining this; pull's `action` gives the same guidance, its summary
reports `reason=cloud_license_missing`, and its exit code is 1. Malformed or
oversized licenses retain their existing validation failures.

Every reserved rendezvous identifier (RID) is kept for the life of the process,
including failed attempts, so a later attempt cannot reuse it. The local safety
ceiling is 16,777,216 reservations. `iotc_rid_history_exhausted` means that
history is full: new session setup fails and ordinary cleanup still runs.
Waiting or retrying in the same process cannot free a reservation. Stop the
process normally, wait for session release and exit, then restart it to restore
the budget. [This is a local limit, not a device refusal.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#attempt-and-cache-lifetimes) The record uses the
selected log format and sink; a stalled sink can drop it.

Client class is `loopback`, `LAN` for private/link-local IPs, or `other`;
client addresses are omitted. Channel byte counts include successful partial
writes; duration ends when both forwarding workers finish.
Outage reminders continue without clients and during an in-flight dial or
blocked cleanup. Readiness resets the outage; intentional idle release does
not start one.

A local client may close after one reply while the device keeps sending events.
If a local socket write reports a broken pipe, connection reset, or closed
connection, the bridge closes that channel at INFO with `reason=local_closed`.
Device-side broken-pipe and connection-reset failures still produce WARN
diagnostics. Closed connection handles during teardown remain quiet.

Final proxy/pull diagnostics carry independent `reason`, `phase`, and
`cleanup_reason`, plus up to eight distinct `leaf_causes`. `more` marks omitted
labels/nodes. DEBUG `diagnostic_causes` retains up to 64 classified graph nodes.
Raw wrapped dependency errors are not printed.

### Proxy final failure

When the proxy exits on a classified failure, stderr has exactly one JSON
record with `msg="proxy failed"`, a string `reason`, and a string
`cleanup_reason`. `cleanup_reason="none"` means cleanup succeeded or no session
was held. A failed release is reported here even if an earlier session log
already reported the primary failure. The selected primary stays in `reason`;
cleanup does not erase it. Configuration refusals use the same record and
include the fixed option and rule.

This record is JSON even with `--log-format text`, and appears at every
`--log-level`. Orderly shutdown, including a signal followed by successful release, has
no failure record. An error returned during canceled startup still exits 1
and has a failure record. As with other stderr output, a forced kill or a blocked or
broken sink can prevent delivery. Readers must ignore added fields and use
[the reason table](#status-and-reason-values) to interpret the values. This
record name and those two keys are the only log record contract; lifecycle
messages and hints can change.

### Transport telemetry

With collection enabled, DEBUG includes `rdt_stats`, `transport_stats`,
`tunnel_stats`, and at most 64 transport event records per interval.
`debug_dropped` counts suppressed records. Event names are `rdt_event`,
`tunnel_flow`, `iotc_receive_oddity`, `rdt_receive_oddity`,
`rdt_receive_pressure`, and `tunnel_receive_oddity`. Counters reset per transport attempt.
With collection disabled, pressure/oddity debug events have a fixed 64-record
budget per attempt and no periodic timer.

- `rdt_stats`: admitted/delivered bytes, congestion window, queued/unsent/in-flight
  data, [ACK/SACK ranges, retransmission causes, reordering/gaps, and RTT quality.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-processing-at-the-sender)
- `transport_stats`: secure-send queue residence, blocking writes, and owner
  submission/dispatch wait.
- `tunnel_stats`: data admissions, consumption/discards, PAUSE/RESUME/WAKE
  decisions, pressure/high/low water, desired/accepted pause state, pending
  controls, accepted handoff prefix, queue peaks, paused time, delivery age.

Queued controls count retained decisions that may later cancel; sent controls
count complete RDT acceptance. Backlog age covers the current nonempty period.
Telemetry uses numeric counts/durations/channel numbers, without payloads or
session identities. Extremely large positive intervals saturate at the owner
clock's final instant and retire sampling after one batch.

### Status and reason values

This is the one vocabulary table for command summaries and logged `status`,
`reason`, and `cleanup_reason` fields.
Recovery hints are advice, not stable machine-readable text.

The exit column describes a terminal outcome. A handled retry, channel close,
or debug diagnostic can carry the same reason while the process keeps running;
`—` means the value only describes such an event. For pull, a failed release or
uncertain ownership takes precedence (1), then configuration (2), interruption
(130), busy/lock (3), and other failure (1). Successful work exits 0, except
`nothing-new` with `--error-on-empty` (4). Proxy configuration exits 2, other
failures 1, and orderly shutdown 0, including handled busy retries. Always use
`exit_code` in the pull summary; treat an unknown reason as an unspecified
failure. Ignore unknown JSON fields.

`<empty>` denotes the empty string. Relay diagnostic rows with spaces are
fragments: `relay_failure_summary` joins refusal labels with `; ` in their
listed source order and may prefix a stalled-stage message. `%d` is a decimal
request count. These prose diagnostics are outside the versioned log contract.

| Field | Value | Exit when terminal | Meaning or next step |
|---|---|---|---|
| `cleanup_reason` | `none` | any primary code | No cleanup failure; the primary outcome chooses the exit. |
| `cleanup_reason` | `release_failed` | 1 | Cleanup failed, overriding success, busy, and interruption. |
| `reason` | `<empty>` | — | Initial session lifecycle reason before any outcome. |
| `reason` | `address_in_use` | 1 | Address in use. |
| `reason` | `address_unavailable` | 1 | Address unavailable. |
| `reason` | `another relay request` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `authentication rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `authentication unsupported` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `authentication_unsupported` | 1 | Authentication unsupported. |
| `reason` | `broken_pipe` | 1 | Broken pipe. |
| `reason` | `busy` | proxy 1; pull 3 | Wait for the remote owner or use `--busy-wait` for direct pull. |
| `reason` | `canceled` | proxy 1; pull 130 | Interruption; `channel_close` reports Serve cancellation even if stream teardown finishes first. Proxy cancellation with successful release exits 0. |
| `reason` | `capacity` | 1 | A local resource budget was exhausted. |
| `reason` | `case_collision` | 1 | Resolve case or Unicode name collisions in the source and archive. |
| `reason` | `catalog capacity exceeded` | 1 | Catalog capacity exceeded. |
| `reason` | `cloud_license_missing` | 1 | Cloud allocation omitted its SDK key; report the service-side change. |
| `reason` | `connection timeout` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `connection_closed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `connection_refused` | 1 | Connection refused. |
| `reason` | `connection_reset` | 1 | Connection reset. |
| `reason` | `connection_timeout` | 1 | Connection timeout. |
| `reason` | `descriptor_limit` | 1 | Descriptor limit. |
| `reason` | `device asleep` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `device offline` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `device refused` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `device unknown` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `device_offline` | 1 | Device offline. |
| `reason` | `disk_full` | 1 | Free space on the destination filesystem, then retry. |
| `reason` | `dns_not_found` | 1 | DNS lookup found no address. |
| `reason` | `drain_opportunity` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `dtls_required` | 1 | Peer requires DTLS. |
| `reason` | `ended` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `frame_too_large` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `future_mtime` | 1 | Check the device and host clocks, then retry. |
| `reason` | `handshake_failed` | 1 | Handshake failed. |
| `reason` | `high_water` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `idle` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `inbox_capacity` | 1 | The session event inbox is full. |
| `reason` | `incomplete_frame` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `inconsistent server answer: detail identity differs from requested pair` | 1 | Inconsistent server answer: detail identity differs from requested pair. |
| `reason` | `invalid_configuration` | 1 or 2 | Normalized session failure exits 1; direct configuration failure exits 2. |
| `reason` | `invalid_device_detail` | 1 | Invalid device detail. |
| `reason` | `invalid_frame` | 1 | Invalid frame. |
| `reason` | `invalid_identifier` | 1 | Invalid identifier. |
| `reason` | `invalid_license` | 1 | Invalid license. |
| `reason` | `invalid_metadata` | 1 | Check source timestamps and file sizes, then retry. |
| `reason` | `invalid_method` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `invalid_option` | 2 | Option, environment, state-directory, or catalog-selection refusal. Underlying filesystem, DNS, and timeout classes are replaced by this value. |
| `reason` | `invalid_request` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `invalid_response` | 1 | Invalid response. |
| `reason` | `inventory_limit` | 1 | Over 200,000 entries per side (folders included), or over 64 MiB of source path text before filtering. SMB reads pages of 256 entries; `--only` cannot reduce this budget. |
| `reason` | `layout_mismatch` | 2 | Use another destination; explicit --dir and default MyWorks layouts cannot share state, even with --rebind. |
| `reason` | `liveness_timeout` | 1 | The device stopped answering keepalives. |
| `reason` | `local_close` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `local_closed` | — | The local client closed; later device events could not be written. No warning or session failure. |
| `reason` | `local_queue_capacity` | 1 | The tunnel send queue is full. |
| `reason` | `local_write_failed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `lock_held` | 3 | Wait for the other destination writer. |
| `reason` | `login_rejected` | 1 | Login rejected. |
| `reason` | `low_water` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `metadata_limit` | 1 | Restore or move destination state below 64 MiB. |
| `reason` | `metadata_permissions` | 1 | Restore `.seestar-wormhole` to 0700 and its files to 0600 on Unix. Share the images without widening metadata access; see Windows permission rules for that platform. |
| `reason` | `missing_directory` | 1 | Check that every --dir exists under EMMC Images on the device. |
| `reason` | `mixed_decisions` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `multiple device logins` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `no_space` | 1 | No space. |
| `reason` | `none` | 0 or 4 | No primary failure; `--error-on-empty` can select 4. |
| `reason` | `not_a_directory` | 1 | Not a directory. |
| `reason` | `not_found` | 1 | Not found. |
| `reason` | `operation_failed` | 1 | Operation failed. |
| `reason` | `operation_panic` | 1 | Report the pull failure, then retry after checking session release. |
| `reason` | `owner_closed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `ownership_uncertain` | 1 | Ownership could not be confirmed; check release before reconnecting. |
| `reason` | `packet_delivery_failed` | 1 | Packet delivery failed. |
| `reason` | `peer_close` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `peer_closed` | 1 | Peer closed. |
| `reason` | `peer_io_failed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `peer_replaced_setup` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `permission_denied` | 1 | Permission denied. |
| `reason` | `port rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `read_failed` | 1 | Read failed. |
| `reason` | `read_only` | 1 | Read only. |
| `reason` | `reauthentication_required` | 1 | Reauthentication required. |
| `reason` | `receive_budget` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `rejected` | 1 | Rejected. |
| `reason` | `relay accepted; relay info never arrived after %d requests` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay accepted; relay info request never sent` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay info received; relay selection did not complete` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay pending; relay never became ready after %d requests` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay requested; no usable result after %d requests` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay result never arrived after %d requests` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `relay unsupported` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `release_abandoned` | 1 | Bounded cloud stop was abandoned or unavailable; see [session ownership policy](../DESIGN.md#session-ownership-implementation) for retry and final-release behavior. |
| `reason` | `release_failed` | 1 | Release or local cleanup failed; check ownership before reconnecting. |
| `reason` | `remote_changed` | 1 | Wait for source writes to finish, then retry. |
| `reason` | `send_capacity` | 1 | The secure-send queue is full. |
| `reason` | `server refused` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `server_refused` | 1 | Server refused. |
| `reason` | `setup_rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `site_protected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `source_mismatch` | 2 | Use the original source, choose another destination, or use --rebind to take over. |
| `reason` | `state_invalid` | 1 | Restore or move corrupt/unsupported destination state explicitly. |
| `reason` | `step_budget` | 1 | A session step exceeded its action budget. |
| `reason` | `symlink_rejected` | 1 | Symlink rejected. |
| `reason` | `timeout` | 1 | Timeout. |
| `reason` | `transport failed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `transport_failed` | 1 | Transport failed. |
| `reason` | `transport` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `unauthorized` | 1 | Unauthorized. |
| `reason` | `unsafe_path` | 1 | Resolve unsafe names, entries outside the listed source directory, symlinks/reparse points, or nonregular files. Unsafe source entries fail even outside `--only`, without new receipts or a checkpoint. |
| `reason` | `unsupported` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `verify_mismatch` | 1 | Check source stability and storage, then retry; prior archive bytes are retained. |
| `reason` | `write_failed` | 1 | Write failed. |
| `status` | `busy` | 3 | Device busy or destination locked. |
| `status` | `canceled` | 130 | Interrupted after cleanup. |
| `status` | `copied` | 0 | Files copied. |
| `status` | `deferred` | 0 | Files are still settling. |
| `status` | `error` | 1 or 2 | Work, cleanup, or configuration failed. |
| `status` | `nothing-new` | 0 or 4 | No new work; 4 with `--error-on-empty`. |
| `status` | `planned` | 0 | Dry-run plan, including an empty plan. |

Pull contains panics in owned operations and guarded backend callbacks,
cancels work, and releases its session. This does not contain arbitrary panics
in library-owned goroutines. See [development](https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/development/README.md).

## Traces and capture keys

Both are off by default and require a new path. Existing files and symlinks
are refused. New files use `0600` on Unix or protected Windows ACLs.
`--trace` creation failure stops startup; `--keylog` creation failure warns and
disables capture. Recording after startup is bounded and best effort.

### Redacted trace

`--trace run.jsonl` records versioned JSON Lines transactions: one input and
its ordered outputs, machine/instance, sequence, monotonic owner time, and
UTC capture time. Coverage includes account/session, IOTC, RDT, tunnel,
allocation, DTLS-adapter, and LAN boundaries. Library-internal Pion handshake
steps and socket scheduling are outside it.

Lengths, event/action kinds, available opcodes, RDT header sequence numbers,
and local counters remain. IPs become stable per-trace peer aliases. Only
configured device destination ports keep numbers. Client, listener, transport,
NAT, ephemeral, and unmapped ports get aliases even if their number equals a
mapped service. Peer/port alias tables each hold 128 entries, then use
`peerOverflow`/`portOverflow`. Payloads, keys, PSKs, tokens, passwords, UIDs,
serials, account IDs, and site values are discarded. Errors become categories.
Review the operational metadata before sharing a trace.

Limits are 64 queued transactions, 256 KiB encoded per transaction including
newline, 2048 actions per transaction, and a 16 MiB file. Queue plus worker
encoded content is at most 16.25 MiB, plus bounded encoding scratch/overhead.
Producers serialize projection but never wait for storage or queue space.
Lossless accepted records have contiguous sequence numbers.

`dropped` is cumulative. A lossy final `end` adds nonzero `drop_causes` for
`queue_full`, `transaction_limit`, `invalid`, `closed`, `file_cap`, or `storage`.
`contention` is retained for older traces; new writers never increment it.
Older traces have no breakdown, and dropped steps have no retained identity
or timestamp. A short/failed write can leave one torn final record; the sink
is then abandoned and remaining queued steps count as lost. Trailer-write and
close failures add losses that cannot appear in an already written trailer.
Final drain allows two seconds after protocol cleanup. A stalled syscall may
hold one bounded worker until storage recovers or the process exits.
Missing `end` or any drops prevents exact replay. Redacted traces cannot
reconstruct erased bytes/secrets; see [development](https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/development/README.md).

### Sensitive keylog

`--keylog /path/outside/checkout/window.keys` saves Pion NSS DTLS 1.2 lines
in this local format:

```text
CLIENT_RANDOM <client-random-hex> <master-secret-hex>
# IOTC_KEY <attempt-decimal> <RID-hex> <key-ref-decimal> <AES-key-hex> <IV-hex>
```

The spec describes [DTLS master-secret derivation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#key-schedule).

RID is 8 bytes, AES key 16 bytes, IV 12 bytes; hex is lowercase. References
are local log handles; the [recorded pairs follow the spec’s attempt/cache lifetime](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#gcm-key-iv-and-cache-contract).
Handle 3 is the master pair for type-1 replies; later references are server
pairs for types 2/3/4/14. New pairs are recorded before use,
including metadata refreshes; cached reused pairs are not repeated.

The nonblocking queue holds 32 lines of at most 256 bytes. One worker writes
without userspace buffering or per-line fsync. Full queues/write failures
drop lines and warn once with the count observed then; later writes keep
trying. Capture failures never affect handshakes, retries, or session state.
Final drain allows two seconds after session/cloud cleanup. A stalled worker
can survive until storage recovers or process exit.

A key log plus a packet capture can decrypt everything that session carried,
including settings and credentials the telescope returns to clients (for
example, network settings), not only images and commands. Treat both files
as secrets: keep them outside the checkout, delete them when done, and never
attach either to an issue.

The proxy records no packets and decodes no captures; development has
separate offline capture tools.

## Containers

The scratch image runs as UID/GID `65532:65532`, with a static binary and CA
certificates. Its entrypoint is:

```text
/seestar-wormhole proxy --bind 0.0.0.0 --state-dir /state
```

Mount `/state` writable by that identity. Only the installation UUID lives
there. No capabilities are needed for default ports. The device keeps its own site and clock. Later scalar flags override
defaults, including `--state-dir ""` for memory-only identity. Container/VM networking must deliver
the phone's actual LAN broadcasts for discovery. See [always-on hosts](always-on.md).

## Windows

The bind default is also `127.0.0.2` on Windows. Use native absolute drive or
UNC destinations; `C:relative` is refused. Quote regexps for your shell:
PowerShell accepts `--only '^M 31$'` as Unix shells do; cmd.exe needs double
quotes, `--only "^M 31$"`. The built-in SMB client cannot choose port
4445; use pull or a client with a configurable SMB port.

State, metadata, traces, and keys must be owned by the current user and have a
protected DACL containing exactly explicit full-access allow ACEs for that user
and SYSTEM, with no ACE flags or other entries (one ACE when running as
SYSTEM). Unsafe existing ACLs and reparse points are refused, never repaired.
A refused ACL on pull metadata reports `operation_failed`, not
`metadata_permissions`, which is Unix-only. Use a
filesystem supporting Windows ACLs and trusted parent directories.
Unsupported private storage fails closed.

Ctrl+C or Ctrl+Break requests cleanup; wait for exit. Console close, logoff,
and shutdown allow only best-effort cleanup before OS termination. Windows
stderr logging uses a bounded queue and drops lines when the sink stalls.
`--lan-discovery` fails validation because packet metadata needed for safe
interface/source selection is unavailable; TCP forwarding remains available.
For build/runtime coverage, see [status](status.md).

## LAN discovery

Enable `--lan-discovery` with a literal LAN IPv4 bind or `0.0.0.0`. Loopback,
IPv6, and hostnames are refused for discovery. An explicit address must belong
to an active nonloopback broadcast interface. Wildcard selects all qualifying
IPv4 addresses at startup. TCP still follows `--bind`; UDP 4720 uses a filtered
wildcard socket so it can receive directed and limited broadcasts.

Discovery uses selected cloud detail for serial, name (`ssid`), and model.
Lazy mode fetches detail without allocating a session. Replies are unicast to
the scan's original source port, sourced from the matching local LAN address
and UDP 4720. [The app uses the reply's source IP, not a JSON IP field.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#accepting-a-reply)

Requesters must be on the selected current IPv4 prefix. Network and directed
broadcast source addresses are excluded for prefixes wider than `/31`;
`/31` and `/32` have no reserved broadcast/network boundary. Destination and
requester subnet select the reply source. Ambiguous broadcasts matching several
local addresses get no reply; use an explicit bind or unicast scan.

Oversize datagrams (over 4096 bytes) and malformed or non-object JSON are
ignored; accepted scans follow the [request validation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#request) and the [discovery dispatcher](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#device-side-responder)
with a synthesized identity. There are four immediate
reply tokens, then one per second globally. Live interface inspections have
a separate four-immediate/one-per-second budget, including failed admissions.
Malformed scans or an exhausted reply bucket do no interface inspection.
Only successful source admission spends a reply token.

Before replying, discovery rereads ingress interface addresses. Removed or
inactive interfaces, lookup failures, and removed selected addresses get no
reply. Prefix changes for an existing selected address apply immediately.
New/replacement addresses require a restart, including wildcard mode. A reply
write has a 250 ms deadline. This bounded responder is not a flood defense.

Discovery is synthesized from cloud detail; this differs from the
[recommendation to forward the real result](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#stand-in-responders).
The reply includes cloud serial/name, a `product_model` synthesized from the
cloud label, and inferred `is_verified:true`. Empty `ssid` falls back to model;
missing/null `ssid` fails cloud validation. No password, lock, or client count
is invented. `Timestamp` is proxy elapsed seconds plus nine fractional digits.
Synthesized field names are logged at WARN. These values do not establish
hardware activation or master status.

Allow the proxy through the host firewall, grant app local-network access,
and disable Wi-Fi client isolation between phone and host. Duplicate discovery
records for the same serial can compete in the app's device list. Choose the
LAN entry while the proxy owns the remote session. Device events pass through
without filling missing fields or [imposing reply/event order](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake/liveness-and-ordering.md#ordering). Forwarding is
TCP only; discovery does not create RTSP-negotiated UDP forwarding or confer
phone master/guest identity. See [phone app](phone-app.md) and
[troubleshooting](troubleshooting.md).

## JSON file compatibility

Pull state and receipts, selection hashes, trace records, trace-diff reports,
and generated license manifests retain their existing JSON layout: struct
field order, sorted map keys, indentation or line terminators, escaped HTML and
JavaScript characters, and null versus empty lists. JSON v2 uses explicit
options and zero-value omission tags to preserve those bytes.

Readers match documented field names exactly, including case; other spellings
are unknown members. All JSON readers reject duplicate names and invalid UTF-8
(including invalid Unicode escapes). Null still means absent for optional
pointer fields and zero for scalar fields decoded into fresh structs. Encoders
reject invalid UTF-8 rather than substituting replacement characters; a pull
state encoding failure follows the existing metadata-write failure path.
