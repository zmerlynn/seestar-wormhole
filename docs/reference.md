# Command reference

Use this page when you script seestar-wormhole or need an exact option or
failure rule. For setup examples, see [phone app](phone-app.md),
[always-on hosts](always-on.md), [copying images](copying.md), and
[other clients](clients.md). The [protocol guide](protocol.md) connects the
proxy's wire behavior and local policy to the v1 specification.

- [ui](#ui): the UI, in a native window
- [proxy](#proxy): listeners and flags
- [discovery](#discovery): scan replies without a cloud session
- [licenses](#licenses)
- [Global command behavior](#global-command-behavior) and [exit codes](#exit-codes)
- [Availability and busy devices](#availability-and-busy-devices)
- [Site protection](#site-protection)
- [Device selection](#device-selection) and [installation identity](#installation-identity)
- [Logs](#logs)
- [Verifying a release](#verifying-a-release)
- [Containers](#containers), [Windows](#windows), [LAN discovery](#lan-discovery)

## ui

```sh
seestar-wormhole
seestar-wormhole ui [--state-dir PATH]
```

![The UI before connecting: Connect, Use with, and the account fields](images/gui-setup.png)

Bare launch of the UI executable opens the UI, as does `ui`. Only UI builds
(`gui,production` in releases) have `ui`, and they show it in a native window.
The CLI has no UI command and never opens a browser. Enter credentials and
options, then press Connect. Loading settings never starts a proxy. Stop releases
the session and leaves the page usable; Quit or Ctrl+C releases it and exits.
Closing the terminal window also quits; on Windows, cleanup is best effort within
the OS deadline.
A credential or authorization rejection ends the run after cleanup. Correct the
credentials and press Connect again. Closing the native window requests quit and waits for bounded release.
Native application quit and confirmed Windows session end also join release;
a canceled Windows session end leaves the app running. The OS can impose a
shorter shutdown deadline. Launch again with the same state directory to bring
the native window forward. A separately running `proxy` command is independent;
conflicting forwarding ports must be freed before starting another run.

Advanced shows ten fixed service rows with Local port, Seestar port, and
Purpose columns. Local ports are on your computer. Leave **Local port** empty
to turn off a service. Each enabled local port must be unique and between 1 and
65535. **Phone on this network** uses local port 80 for Seestar port 80;
**This computer only** uses 8080. SMB uses local port 4445 for Seestar port 445.
The CLI retains arbitrary port mappings.
Existing state files are never overwritten on a failed load.

The default state root is `os.UserConfigDir()/seestar-wormhole`: on Linux,
`$XDG_CONFIG_HOME/seestar-wormhole` or `$HOME/.config/seestar-wormhole`; on macOS,
`$HOME/Library/Application Support/seestar-wormhole`; on Windows,
`%APPDATA%\seestar-wormhole`. `--state-dir PATH` selects an existing installation
root without moving or copying its identity. The files are:

| File | Purpose |
|---|---|
| `config.json` | Non-secret settings; see the version 1 format below. |
| `InterProcessKV/device_uuid` | The existing [installation identity](#installation-identity), shared with `proxy --state-dir PATH`. No second identity or migration. |
| `gui.log`, `gui.log.1` | Sanitized GUI events as JSON lines (UTC time, message, code, classified phase/reason), at most 64 KiB each, with one rotation. |
| `ui.lock` | Process-lifetime UI lock. |
| `ui-runtime.json` | Private port and per-process control token; removed on orderly exit. |
| `webview-<random>/` | Fresh private Windows GUI engine profile for each launch. Best-effort removal after window shutdown and on later launches; locked stale profiles never block startup. |
| `ui-open.html` | Private browser bootstrap with the control capability; the opener receives only its path. Removed when the owning process exits normally. |

`config.json` uses version 1: `version` is required and must be `1`; absent members default to `bind: "127.0.0.1"`, `discovery: false`, `idle: "0"` (a nonnegative Go duration string, such as `"2m"`), `serial: ""`, `model: ""`, `ports: []` (standard service mappings), `email: ""`, and `remember_password: false`. Port overrides are objects with lowercase numeric `local` and `device` members (0–65535; local 0 disables a service, device must name a standard service). Unknown members are preserved on save. Reads are limited to 64 KiB and never rewrite the file; a missing or unsupported version, including a newer version, is refused and left untouched.

Successful Connect saves its non-secret settings snapshot atomically: after the first Ready state with local listeners/discovery initialized in Keep connected mode, or after authentication, telescope selection, and local initialization in connect-when-needed mode. Failed attempts preserve the last saved settings. A storage failure keeps the connection running and offers Retry saving settings for that snapshot. A newer successful save supersedes an older retry. On Unix the directory must be
private (`0700`) and files `0600`; unsafe existing permissions or links are
refused. Windows uses the same protected current-user/SYSTEM ACL policy as
installation identity. Parent directories must be trusted. The CLI's default
identity remains memory-only; pass the same state root explicitly to share it.

Details shows effective settings as JSON and recent events in local time. Copy
diagnostic summary copies one JSON object with app/version, those settings, and
events in UTC. It excludes account/device identifiers, addresses, secrets, and
paths. The private log path remains visible on the page.

The page uses an independent `127.0.0.1:0` listener even when the proxy bind is
wider, with a random 256-bit token in the URL fragment and a token header on every
API request. Exact Host checks, exact Origin checks on JSON POST writes, bounded
requests, no framing, a restrictive content policy, and no-store/no-referrer
headers protect the control page from other websites; local processes running
as the same user remain trusted. Treat the private URL as a control capability.
Browser openers receive a private HTML file that redirects to the page, so the
capability is absent from opener arguments. If opening fails, the app prints
the private URL for manual use; keep that output private. The bootstrap can
remain after a crash and is replaced on the next browser launch.

Known native-window limitation: Wails currently exposes no cancellable policy
hooks for navigation, new windows, or external drops on all three platforms.
Dropping a file or link can replace the controls with another page; an engine
popup can open another view or browser window. A page from another origin cannot
read the control capability from the original page or make authenticated control
requests. The Wails command bridge is blocked: its separate asset/runtime
transport returns 404. These protections do not prevent navigation or popups;
relaunch the app to return to its controls. A second launch restores the original
control page before showing and focusing the existing window. Native context menus
retain the engine defaults.

The native engines also manage browser data. Windows uses `webview-<random>/` above
and waits up to two seconds for profile cleanup, printing a warning if files
remain locked without changing the normal exit status. Each launch uses a new
profile and attempts to remove older profiles, including the former `webview/`.
Leftover engine cache and session files remain under the private state root
until a later launch finds them unlocked (or the user removes them after exit).
Profiles with unsafe permissions or links are left untouched for manual removal.
macOS WKWebView and Linux WebKitGTK use their default persistent stores: the vendored Wails options expose no nonpersistent data store or
ephemeral network session. Those stores are outside the state root and are not
cleared on exit. The page uses sessionStorage for reload authorization and sets
no cookies or localStorage; no-store headers do not guarantee that an engine
writes no session/cache data. Engine file retention needs native verification.

First run selects **Phone on this network**: all-network listening, LAN
discovery, and local port 80 to Seestar port 80. The trust warning appears
before Connect; loading never starts a session. **This computer only** selects
loopback, disables discovery, and uses local port 8080 for HTTP. Existing
saved settings retain their exposure and appear as **This computer only** or Custom.
Advanced contains telescope serial/model selection, connection timing and idle
minutes, Listen on, discovery, and the fixed port table. Editing network or port
choices selects Custom. Choosing another setup immediately replaces those
network and port settings.

Passwords stay in memory unless **Save password to keychain** is explicitly
chosen. Connect never writes a typed password to keychain. A successful save
immediately persists the email and saved-password association, without signing
in. Relaunch shows **Saved in keychain** without reading the secret or prompting.
Connect reads it with the existing three-second bound. The saved input is
disabled and gray. To change the password, remove the saved password, then type
and optionally save its replacement.
**Remove saved password** clears memory and requests keychain deletion; failure
retains the association and permits **Retry removal**. Removal updates the last saved settings without committing unrelated form edits. A timeout is uncertain:
the OS request may still finish, and further keychain mutations wait for it.
There is no plaintext fallback and no secret is returned to the page.

One primary button changes from **Connect** to **Cancel**, **Stop**, or disabled
**Stopping…**. Running replaces setup with effective read-only ports, a muted
listen/discovery/timing summary, and small Seestar app instructions when discovery
is on. **Details and logs** contains effective settings JSON, bounded sanitized
events in local time, the private log path, and **Copy diagnostic summary**.
The copied JSON excludes addresses, paths, credentials, tokens, account/device
identifiers, and payloads. Logs contain fixed messages and categories with UTC
time and classified failure phase/reason; no raw protocol traces. Startup errors also
use the existing native error presentation. Windows window-engine failures
explain that WebView2 Runtime is required.

The rolling log appends fixed events without decoding prior disk history. Existing
files must pass the private-file checks and remain within the size cap; an
oversized file is refused without modification. Only events from this launch
appear in the page and copied summary.

**Quit app** stops the proxy and closes the app; closing the window also
quits. Field errors identify the setting to correct. Lost controls retain
the last known state and warn that the proxy may still run.
Linux sandbox prerequisites are in [troubleshooting](troubleshooting.md#linux-native-window).

**Use saved settings headless:** after successful Connect or explicit password
save, run `seestar-wormhole-cli proxy --config` (add `--state-dir PATH` for a custom
UI root). It reads the same [version 1 `config.json`](#ui) without rewriting it and shares the installation identity.
Explicit flags override saved choices, including discovery; each `-p` replaces the saved override for
that telescope port. `SEESTAR_EMAIL` overrides the saved email. The password
comes from `SEESTAR_PASSWORD` when set, otherwise the OS keychain entry for that
email under service `seestar-wormhole`. A missing or unavailable password exits
with code 2 and a fixed message; keychain reads have the three-second bound.
This command never writes settings or takes or writes `ui.lock`. GUI and
headless runs still follow the account's one-session rule.

On macOS, a launchd user agent works in the user's login session and may prompt
once to allow keychain access; choosing Always Allow permits later reads.
On Windows, scheduled tasks can read Credential Manager as the same user while
logged on; “Run whether user is logged on or not” uses a separate batch logon.
A password-backed batch logon can read persistent same-user credentials, while
S4U (“Do not store password”) cannot access encrypted credentials: use
`SEESTAR_PASSWORD` instead.
Headless Linux/systemd usually has no Secret Service session; supply
`SEESTAR_PASSWORD` through the [credentials-file recipe](always-on.md#credentials-for-a-service),
while the email can still come from `config.json`.

The status distinguishes **Connected to your Seestar** from **Waiting for an
app to connect**. Busy and transport recovery say **Retrying automatically**.
Cancel remains available during first connection; Stop joins cleanup.
Ordinary completion says **Stopped. Seestar may still be releasing the session.**
An uncertain release says **Session release could not be confirmed. If Seestar
is occupied, wait and try Connect again.** Sign-in rejection reopens
credential correction without deleting a saved password. Bind, discovery, HTTP
permission, and port-conflict failures have separate safe error codes.

## proxy

```sh
seestar-wormhole-cli proxy
```

For native controls, use [ui](#ui).

The app cannot set the device's site, clock or time zone through the proxy.
See [site protection](#site-protection). The command takes no positional arguments. It forwards TCP through one shared
remote session. Clients can issue device commands and read files; there is no
local authentication or read-only command whitelist. Local connections are
plaintext. This is not an ALPACA server. Keep listeners on
loopback or a trusted network behind a firewall.

### Proxy flags

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `false` | Read the [UI's saved settings](#ui) and share its state root and installation identity. Flags and environment credentials override saved choices. |
| `--bind` | `127.0.0.1` | TCP bind address, resolved by the OS. Discovery requires a literal LAN IPv4 address or `0.0.0.0`. |
| `--port local:device`, `-p` | See mappings below | Repeat to replace one device port's listener or add a device port. Local `0` disables that device listener. |
| `--idle` | `0` | Nonnegative Go duration. Zero connects eagerly and holds the session. Positive values connect on demand and release after the last client has been absent this long. |
| `--pause-threshold` | `4194304` | Per-channel receive-pressure high water in bytes, from `2` through `8388608`. See [flow control](#flow-control). |
| `--lan-discovery` | `false` | Answer UDP 4720 scans on selected LAN interfaces. |
| `--lan-discovery-all-interfaces` | `false` | Include VPN and virtual adapters; requires `--lan-discovery`. |

See also [identity and logging flags](#identity-and-logging-flags).

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

All forwards share 128 client slots with no idle timeout, so restrict who can
reach the listeners. Closed channel IDs are held for at least five minutes, so
hundreds of short-lived connections within five minutes can temporarily exhaust
channel IDs; polling clients should reuse a connection or back off.

Bulk data and RPC replies share the device's ordered send path. Local flow
control cannot move a reply ahead of [bytes the device has already queued](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause).
See [copying images](copying.md) for transfer settings.

## discovery

```sh
seestar-wormhole-cli discovery --bind '<home-lan-ip>' \
  --advertise-serial '<serial>' --advertise-model '<model-label>' \
  --advertise-name '<device-name>'
```

Answers UDP 4720 scans only. It opens no TCP listeners, reads no credentials
(including `SEESTAR_EMAIL` and `SEESTAR_PASSWORD`), and creates no cloud client
or device session. TCP forwarding is separate; see the
[chained home setup](phone-app.md#discovery-at-home-with-a-primary-on-another-lan).
There are no positional arguments, identity files, or status API.

| Flag | Default | Meaning |
|---|---|---|
| `--bind` | `127.0.0.1`, refused | Supply a literal local LAN IPv4 address or `0.0.0.0`; selects eligible interfaces and reply sources. |
| `--advertise-serial` | Empty; required | Serial placed in `result.sn`; no catalog lookup. |
| `--advertise-model` | Empty; required | Model label placed in `result.product_model`; no catalog lookup. |
| `--advertise-name` | Empty | Display name placed in `result.ssid`; empty uses the model label. |
| `--lan-discovery-all-interfaces` | `false` | Include non-broadcast VPN/virtual adapters with the same checks as [proxy discovery](#lan-discovery); no `--lan-discovery` flag is needed. |
| `--log-format` | `json` | `json` or `text` stderr records. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. |
| `--version` | `false` | Print version, revision and platform; other flags are still validated. |

Serial and model must be nonblank. Identity must be valid UTF-8 and encode
within the existing 4096-byte reply limit; validation happens before opening
UDP. Supplied values are advertised verbatim, with only the empty-name
fallback. These flags differ from `proxy --serial/--model`, which select an
account catalog pair. Identity is visible in process arguments and scan
replies. Restart to change it.

The [LAN discovery admission, source selection, rate bounds, synthesis and
platform behavior](#lan-discovery) apply unchanged. Here identity comes from
flags instead of cloud detail. The row does not indicate SSH, primary, or
device readiness. There is no `lan_http_port` warning because this command
cannot inspect the separate TCP forwards.

Exit 0 means orderly shutdown, including cancellation by Ctrl+C or SIGTERM;
exit 2 means a usage/configuration error, including an unavailable UDP bind;
exit 1 means a runtime or logging failure. Configuration errors produce one
JSON `discovery failed` record with `phase=arguments`,
`reason=invalid_option`, fixed `option`/`rule`, a discovery-help hint, and
`cleanup_reason=none`, regardless of log format/level. Other failures also
produce a final JSON `discovery failed` record with `phase`, an existing
[reason value](#status-and-reason-values), and `cleanup_reason=none`.
Orderly shutdown has no failure record.

Lifecycle records are the shared `lan_discovery_started` (INFO, `port` and
`all_interfaces`), `lan_discovery_synthesized` (WARN, `fields` and
`envelope_fields`), `lan_discovery_refused`, and
`lan_discovery_delivery_failed`; see [logs](#logs) for the latter records and
logging-loss handling. No serial, name, model, or credentials are logged.

## licenses

```sh
seestar-wormhole-cli licenses
```

Prints the project NOTICE, then module paths, SPDX identifiers, and full
license/NOTICE texts for third-party libraries linked into this binary.
It needs no credentials, network, or source checkout. It takes no flags except
`--help`/`-h` and no positional arguments. Usage errors exit 2; output failure
exits 1. See [NOTICE](../NOTICE) for attribution.

## Global command behavior

Bare `seestar-wormhole` opens [ui](#ui) and stays running until Quit or Ctrl+C.
Bare `seestar-wormhole-cli` prints help and exits 0 without opening a UI or browser.
Use `help [command]` or
`--help`/`-h` for help. Root `--version` prints
`<program> <version> (<revision>, <os>/<arch>)` (`<program>` is
`seestar-wormhole` or `seestar-wormhole-cli`); unset build metadata is
`development`. Help/version require no credentials or network. Version still
validates other supplied flags.
See the [versioning policy](versioning.md) for the release contract.

Operational flags are local to their command (`ui`, `proxy`, or `discovery`);
they are not root options.
Long flags use double dashes. The only operational short flag is `-p` for
proxy ports. Boolean flags need no value to enable; use `--flag=false` to
disable. Scalar flags use the last supplied value; repeatable port mappings
accumulate. Durations use Go units such as `2m` and `1h30m`, without a `d` unit.

### Identity and logging flags

These identity flags are local to `proxy`; `discovery` shares its logging and
version flags and has [separate advertised identity flags](#discovery).

| Flag | Default | Meaning |
|---|---|---|
| `--serial` | Empty | Select an account device serial; required for multiple distinct catalog pairs. |
| `--model` | Empty | Exact catalog model; a supplied value must match the selected pair. |
| `--state-dir` | Empty; memory only without `--config`, UI root with it | Persist installation UUID at `<path>/InterProcessKV/device_uuid`. With `--config`, also read this root's `config.json`; an empty value selects the default UI root. Without `--config`, explicit empty selects memory-only operation. |
| `--log-format` | `json` | `json` or `text` stderr records, UTC millisecond timestamps. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. |
| `--version` | `false` | Print version/revision/platform and exit after validation. Also available at root. |

The only application-specific runtime environment variables are
`SEESTAR_EMAIL` and `SEESTAR_PASSWORD`. Without `--config`, `proxy` requires
both and loads no config file. With `--config`, they override the saved email
and keychain password. Credentials are kept in memory and never accepted in argv.
Serial/model inputs must be nonblank, at most 4096 bytes, without NUL, CR, or LF. Email must
be valid UTF-8, at most 4096 bytes, and match
`^\w+([-+.]\w+)*@\w+([-.]\w+)*\.\w+([-.]\w+)*$`.
Password must be valid UTF-8 and 8–32 UTF-16 units.

Flag parsing and settings validation precede credentials and
listeners. Command-line configuration errors emit one JSON error with fixed `option`,
`rule`, and command-help hint, regardless of requested log settings. Supplied
values, paths, coordinates, and unknown option names are omitted. Unknown
flags/positional arguments use `option=arguments`. Startup bind and
identity-storage errors identify `--bind` and `--state-dir`.

### Exit codes

The proxy exits 0 after orderly shutdown, 2 for usage or configuration errors,
3 for terminal cloud credential or authorization rejection, and 1 for other
runtime or cleanup failures. Exit 3 covers HTTP 401/403 denial (including rejected
login), persistent profile challenges after the two permitted renewals, and
reauthentication required. Transport failures, busy responses, generic rejected
statuses, and uncertain ownership do not select exit 3. An exit code describes
the final result, not whether the running proxy retried: a stop-only HTTP 403
that is still unresolved at shutdown exits 3, and reaching ready clears it.
Correct the credentials
or account restriction before deliberately restarting; see the
[service recipe](always-on.md#a-systemd-service).
See [status and reason values](#status-and-reason-values) for the complete mapping.

## Availability and busy devices

The [cloud may report remote allocation as busy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#observed-single-session-enforcement).
Phone LAN use through the proxy shares its session. Before switching to the
app's remote mode, stop the proxy and wait for exit to hand ownership back;
persisted identity does not bypass cloud occupancy.

With default `--idle 0`, proxy connects at startup and retries recognized busy
indefinitely, including the first dial. Equal-jitter delays start at 0.5–1
second and grow exponentially to 15–30 seconds. After a ready session loses
transport, recovery is automatic. No new client or restart is required.
Transport failures on the first eager dial retry. Uncertain ownership or failed
cleanup keeps the proxy running: once cleanup joins, it waits 60 seconds before
allocating fresh. Busy answers then use the same indefinite retry policy.

HTTP 429, 502, 503, and 504 on catalog, detail, profile, and login requests
use the same capped backoff as network failures. Their error-page bodies do
not count as malformed JSON. Login allocates no device session, and a
temporary profile failure preserves the installed token. Recovery continues
without restarting the owner. `Retry-After` does not change the retry delay.
Allocation and stop responses keep their ownership uncertainty and bounded
cleanup, including these statuses. Credential rejections and authorization or
other permanent rejections during sign-in or allocation remain blocking. A
rejection received only from stop, such as HTTP 403 or 409, follows the
ownership recovery policy above. HTTP 401 on stop expires the sign-in, so the
next attempt blocks before allocating.

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
failures use the same 60-second recovery delay in lazy mode. Existing clients
close as they do after transport loss; new demand can wait for the next allowed
dial. Its 90-second busy budget starts at that dial. Without demand, the proxy
remains dormant after the delay and does not allocate. Bounded cloud-stop
abandonment during transport recovery keeps its ordinary backoff; see
[session ownership policy](../DESIGN.md#session-ownership-implementation).

| State | What local clients experience |
|---|---|
| `dormant` | Lazy mode waits for demand. |
| `dialing` | Initial acquisition/lazy clients wait. Eager busy recovery and recovery after ready accept then close clients. |
| `ready` | Forward to the device. |
| `releasing` | Close clients and join bounded cleanup; retryable lazy opens can remain queued within their deadline. |
| `backoff` | Eager retries automatically and closes clients; lazy demand waits for the next allowed dial. |
| `busy` | Fresh lazy demand can retry cloud refusal; permanent rejection or protocol errors remain blocked. Ownership recovery uses `backoff`. |
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
| License parsing | 16 KiB JSON cap; decoded NUL refuses the whole license. Standalone-name selection has the ordering incompatibility described in [deviations](protocol.md#deliberate-deviations). |
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
one matching entry in a complete catalog, a differing explicit `--model` fails
with `model_mismatch`. Unmatched serials or multiple pairs without a serial identify
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

For `proxy` without `--state-dir`, a fresh random UUID exists only in memory. Startup does
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
Supply trusted parent directories. The [UI](#ui) uses its persistent state root
by default and shares this exact identity format.

## Logs

Help, version, and license text go to stdout. Logs/errors go
to stderr. Normal logging uses the selected format and level.
Use WARN for quieter long-running service logs.
Rejected startup suppresses pending dependency records and reports their loss at WARN; diagnostic loss makes the final command result fail.
Most diagnostic records contain fixed classifications and numeric context,
not dependency error text, forwarded payloads, or session secrets. Device
selection masks serials.
`wildcard_bind_warning` intentionally includes interface/address exposure.

| Record | Level | Meaning and useful fields |
|---|---|---|
| `proxy_started` | INFO | Revision, listener count, lazy flag, pause threshold; process serving, not necessarily remote ready |
| `installation_id_ephemeral`, `device_selected` | INFO | Identity storage mode; selected public model and masked serial |
| `session_state`, `session_phase_complete`, `session_release_complete` | INFO | State in `phase`, layer, reason, retry milliseconds, local-client policy |
| `lazy_open_attempt` | INFO | New client-triggered attempt and fixed cause |
| `layer_failed` | INFO for handled busy; ERROR for other failures | Classified phase/reason |
| `session_unavailable` | WARN | Outage reason, `unavailable_ms`, `busy_answers`, every five minutes during eager busy, recovery after ready, or ownership/cleanup recovery, including initial acquisition |
| `cloud_retry` | WARN | Catalog network/service retry and delay |
| `device_selection_refused` | WARN | `rule=model_mismatch`; requested and catalog public model labels (unknown labels redacted) |
| `release_not_attempted` | WARN | Release needs reauthentication |
| `dtls_ready` | INFO | Negotiated version and suite |
| `iotc_rid_history_exhausted` | ERROR | Process-lifetime rendezvous identifier reservation budget is full; fixed `error` includes restart guidance |
| `iotc_license_refused` | WARN | `rule=license_nul`; decoded license contains NUL, with no license material logged |
| `relay_progress`, `relay_summary`, `relay_failure_summary` | INFO | Relay phase, request/result/refusal counters, selection; fixed failure reason in the failure summary |
| `channel_open` | INFO | Device `port`, `local_port`, `channel`, `client_class` |
| `channel_close` | INFO | Same context, `duration_ms`, successful `local_to_device_bytes`, `device_to_local_bytes`, classified reason |
| `channel_closed` | INFO for a closed local writer; WARN for abnormal failures; DEBUG otherwise | Tunnel/bridge detail, direction or channel, reason and classified cause |
| `client_rejected`, `listener_accept_failed` | WARN | Capacity/dial or accept failure, ports and classified reason; `rule=SIO_TCP_INFO_required` identifies a failed or short TCP-state query, which refuses admission |
| `client_canceled` | INFO | Admission canceled before forwarding; ports/client class |
| `site_protected` | WARN | Dropped setter; method, integer ID or `unavailable`, reason |
| `site_reject` | WARN | Invalid frame; method and reason |
| `site_reply_failed` | WARN | Local refusal could not be delivered; method, refusal reason, and cause |
| `lan_discovery_started` | INFO | UDP port, `all_interfaces` mode |
| `lan_discovery_refused` | WARN | Nonzero refusal counts per fixed `rule`, at most once per rule per five seconds, plus pending counts at shutdown; no peer data |
| `lan_discovery_delivery_failed` | WARN | Local deadline/write failure or short write; fixed `rule`. Reply failures drop one reply; read-deadline failures end discovery. |
| `lan_discovery_synthesized`, `lan_http_port` | WARN | Synthesized field names; missing HTTP 80 mapping |
| `wildcard_bind_warning` | WARN | Exposed non-LAN interfaces/addresses and TCP mappings; advisory snapshot, excluding discovery UDP |
| `transport_discard_summary` | WARN | Nonzero discard deltas per `rule` (audit row ID) and `count`, every ten seconds on the owner timer; release flushes the final interval |
| `tunnel_refused` | WARN | Replacement or terminal local write failure; `rule`, `channel`, `discarded_bytes`; the local error carries the discarded byte count |
| `dtls_credentials_refused` | WARN | NUL-containing credentials refused with an error; `rule=R024` |
| `diagnostics_lost` | WARN | Rule and cumulative loss count for startup or stderr rules; stderr failure uses stdout |
| `proxy_cleanup_complete` | INFO | Classified cleanup result |
| `proxy failed` | ERROR, always JSON | One final classified failure, after cleanup; `reason` and `cleanup_reason` are stable keys; startup TCP-state checks add `rule=SIO_TCP_INFO_required` and `requirement`, with `phase=tcp_state_query` or `tcp_state_loopback` |
| `ui failed` | ERROR, JSON | UI command argument refusal; fixed `option`, `rule`, help hint, `phase=arguments`, `reason=invalid_option`. UI startup/storage failures use fixed stderr messages; browser actions report sanitized errors in the page. |
| `licenses failed` | ERROR | Final/configuration diagnostics; option/rule or phase/reason/cleanup details |
| `phase_transition`, `rendezvous_path` | DEBUG | Layer transitions; direct/relay and UDP/TCP path |
| `cloud_catalog_pagination_oddities`, `cloud_receive_oddity`, `cloud_response_rejected` | DEBUG | Bounded cloud parsing/catalog diagnostics |
| `diagnostic_causes` | DEBUG | Bounded classified error graph correlated by diagnostic, phase, reason |

`cloud_license_missing` means [the cloud allocation omitted `sdkLicenseKey`](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link) or
returned it null or blank. Setup fails in the cloud phase, releases any valid
allocation identifier, and blocks automatic redial. It is a service-side change;
local configuration cannot supply the missing key. Error records include a fixed
`hint` explaining this, with `reason=cloud_license_missing` and exit code 1. Malformed or
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
cleanup or recovery delay. Ownership recovery keeps its original
`release_failed` or `ownership_uncertain` reminder reason until ready, including
later busy answers. `session_state` shows `backoff` with `retry_ms=60000` and
the fresh `dialing` transition. Readiness resets the outage; intentional idle release does
not start one.

A local client may close after one reply while the device keeps sending events.
If a local socket write reports a broken pipe, connection reset, or closed
connection, the bridge closes that channel at INFO with `reason=local_closed`.
Device-side broken-pipe and connection-reset failures still produce WARN
diagnostics. Closed connection handles during teardown remain quiet.

Final proxy diagnostics carry independent `reason`, `phase`, and
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
no failure record. An error returned during canceled startup still has a failure
record and uses the exit-code mapping above, including exit 3 for terminal cloud
credential or authorization rejection. As with other stderr output, a forced kill or a blocked or
broken sink can prevent delivery. Readers must ignore added fields and use
[the reason table](#status-and-reason-values) to interpret the values. This
record name and those two keys are the only log record contract; lifecycle
messages and hints can change.

### Status and reason values

This is the vocabulary table for logged `reason` and `cleanup_reason` fields.
Recovery hints are advice, not stable machine-readable text.

The exit column describes a terminal outcome. A handled retry, channel close,
or debug diagnostic can carry the same reason while the process keeps running;
`—` means the value only describes such an event. Proxy configuration exits 2,
terminal cloud authorization rejection 3, other failures 1, and orderly shutdown
0, including handled busy retries.
An unknown reason means an unspecified failure. Readers ignore unknown JSON fields.

`<empty>` denotes the empty string. Relay diagnostic rows with spaces are
fragments: `relay_failure_summary` joins refusal labels with `; ` in their
listed source order and may prefix a stalled-stage message. `%d` is a decimal
request count. These prose diagnostics are outside the versioned log contract.

| Field | Value | Exit when terminal | Meaning or next step |
|---|---|---|---|
| `cleanup_reason` | `none` | any primary code | No cleanup failure; the primary outcome chooses the exit. |
| `cleanup_reason` | `release_failed` | 1 or 3 | Cleanup failed, overriding success, busy, and interruption; a primary cloud authorization rejection keeps exit 3. |
| `reason` | `<empty>` | — | Initial session lifecycle reason before any outcome. |
| `reason` | `address_in_use` | 1 | Address in use. |
| `reason` | `address_unavailable` | 1 | Address unavailable. |
| `reason` | `another relay request` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `authentication rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `authentication unsupported` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `authentication_unsupported` | 1 | Authentication unsupported. |
| `reason` | `broken_pipe` | 1 | Broken pipe. |
| `reason` | `busy` | 1 | Wait for the remote owner to release the session. |
| `reason` | `canceled` | 1 | Interruption; `channel_close` reports Serve cancellation even if stream teardown finishes first. Proxy cancellation with successful release exits 0. |
| `reason` | `capacity` | 1 | A local resource budget was exhausted. |
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
| `reason` | `dns_not_found` | 1 | DNS lookup found no address. |
| `reason` | `dtls_required` | 1 | Peer requires DTLS. |
| `reason` | `ended` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `frame_too_large` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `handshake_failed` | 1 | Handshake failed. |
| `reason` | `idle` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `inbox_capacity` | 1 | The session event inbox is full. |
| `reason` | `incomplete_frame` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `inconsistent server answer: detail identity differs from requested pair` | 1 | Inconsistent server answer: detail identity differs from requested pair. |
| `reason` | `invalid_configuration` | 1 or 2 | Normalized session failure exits 1; direct configuration failure exits 2. |
| `reason` | `invalid_device_detail` | 1 | Invalid device detail. |
| `reason` | `invalid_frame` | 1 | Invalid frame. |
| `reason` | `invalid_identifier` | 1 | Invalid identifier. |
| `reason` | `invalid_license` | 1 | Invalid license. |
| `reason` | `invalid_method` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `invalid_option` | 2 | Option, environment, state-directory, or catalog-selection refusal. Underlying filesystem, DNS, and timeout classes are replaced by this value. |
| `reason` | `invalid_request` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `invalid_response` | 1 | Invalid response. |
| `reason` | `liveness_timeout` | 1 | The device stopped answering keepalives. |
| `reason` | `local_close` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `local_closed` | — | The local client closed; later device events could not be written. No warning or session failure. |
| `reason` | `local_queue_capacity` | 1 | The tunnel send queue is full. |
| `reason` | `local_write_failed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `login_rejected` | 1 | Login rejected. |
| `reason` | `loopback_unavailable` | 1 | The startup TCP-state check could not establish its loopback connection; check local networking. |
| `reason` | `multiple device logins` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `no_space` | 1 | No space. |
| `reason` | `none` | 0 | No primary failure. |
| `reason` | `not_a_directory` | 1 | Not a directory. |
| `reason` | `not_found` | 1 | Not found. |
| `reason` | `operation_failed` | 1 | Operation failed. |
| `reason` | `owner_closed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `ownership_uncertain` | 1 | Ownership could not be confirmed; after cleanup joins, wait 60 seconds before fresh allocation when eager or when lazy demand exists. |
| `reason` | `packet_delivery_failed` | 1 | Packet delivery failed. |
| `reason` | `peer_close` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `peer_closed` | 1 | Peer closed. |
| `reason` | `peer_io_failed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `peer_replaced_setup` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `permission_denied` | 1 | Permission denied. |
| `reason` | `port rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `read_failed` | 1 | Read failed. |
| `reason` | `read_only` | 1 | Read only. |
| `reason` | `reauthentication_required` | 3 | Cloud authorization requires explicit credentials and an operator restart. |
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
| `reason` | `release_failed` | 1 | Release or local cleanup failed; after cleanup joins, wait 60 seconds before fresh allocation when eager or when lazy demand exists. Shutdown still reports failed cleanup. |
| `reason` | `send_capacity` | 1 | The secure-send queue is full. |
| `reason` | `server refused` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `server_refused` | 1 | Server refused. |
| `reason` | `setup_rejected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `site_protected` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `step_budget` | 1 | A session step exceeded its action budget. |
| `reason` | `symlink_rejected` | 1 | Symlink rejected. |
| `reason` | `timeout` | 1 | Timeout. |
| `reason` | `transport failed` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `transport_failed` | 1 | Transport failed. |
| `reason` | `transport` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `unauthorized` | 3 | Cloud HTTP 401/403 denial, including login rejection or exhausted profile renewal; correct credentials or account restrictions, then restart explicitly. |
| `reason` | `unsupported` | — | Diagnostic context; does not itself exit the process. |
| `reason` | `write_failed` | 1 | Write failed. |

## Verifying a release

Each release lists a SHA-256 checksum for every executable and DMG in
`SHA256SUMS`, and carries a GitHub build provenance attestation for every asset.
With the downloaded assets and `SHA256SUMS` in one directory:

```sh
sha256sum --check --ignore-missing SHA256SUMS
gh attestation verify <downloaded-asset> --repo zmerlynn/seestar-wormhole
```

CLI downloads have a byte-for-byte reproducible build promise: identical source,
version, revision, and Go toolchain produce identical executables. See
[release build mechanics](development/releasing.md). UI downloads are verified
by SHA256SUMS and attestation only; they do not carry a byte-for-byte
reproducibility promise. Native toolchains, packaging, and signing affect them.

## Containers

The scratch image runs as UID/GID `65532:65532`, with a static binary and CA
certificates. Its entrypoint is:

```text
/seestar-wormhole-cli proxy --bind 0.0.0.0 --state-dir /state
```

Mount `/state` writable by that identity. Only the installation UUID lives
there. No capabilities are needed for default ports. The device keeps its own site and clock. Later scalar flags override
defaults, including `--state-dir ""` for memory-only identity. Container/VM networking must deliver
the phone's actual LAN broadcasts for discovery. See [always-on hosts](always-on.md).

## Windows

The Windows UI executable uses the GUI subsystem. With arguments it attaches to
the parent console and restores missing stdout/stderr handles; valid file and
pipe redirections are preserved. With no arguments it opens no console. The
shell does not wait for a GUI executable and its exit codes do not reach scripts.
Scripts and services must use `seestar-wormhole-cli.exe`.

TCP forwarding requires Windows 10 version 1703 or later, or Windows Server
2016 or later, for non-consuming reset detection (`SIO_TCP_INFO`).
Before opening forwarding listeners, the proxy queries a connected IPv4 loopback
TCP pair at `127.0.0.1`, independently of `--bind`, and refuses startup with exit 1
and a final ERROR if the query fails, returns a short result, or loopback setup is
unavailable.

Image copying uses
[rclone through the SMB listener](copying.md#windows).

Installation identity files must be owned by the current user and have a
protected DACL containing exactly explicit full-access allow ACEs for that user
and SYSTEM, with no ACE flags or other entries (one ACE when running as
SYSTEM). Unsafe existing ACLs and reparse points are refused, never repaired.
Storage requires a filesystem supporting Windows ACLs and trusted parent
directories. Unsupported private storage fails closed.

Ctrl+C or Ctrl+Break requests cleanup; wait for exit. Console close, logoff,
and shutdown allow only best-effort cleanup before OS termination. Windows
stderr logging uses a bounded queue; lost lines are counted, reported on recovery or stdout, and cause a failed command result.
LAN discovery uses the [shared responder](#lan-discovery).
For build/runtime coverage, see [status](status.md).

## LAN discovery

For the proxy, enable `--lan-discovery` with a literal LAN IPv4 bind or
`0.0.0.0`. The [discovery command](#discovery) uses the same responder without
a cloud session or TCP listeners. Loopback,
IPv6, and hostnames are refused for discovery. By default, an explicit address
must belong to an active nonloopback broadcast interface. Wildcard selects all
qualifying IPv4 addresses at startup. TCP still follows `--bind`; UDP 4720 uses
a filtered wildcard socket so it can receive directed and limited broadcasts.

Add `--lan-discovery-all-interfaces` to include VPN and virtual adapters that
lack the broadcast flag, so peers on those networks can discover the proxy.
Windows types some virtual adapters as non-broadcast even when they support
discovery traffic. This option removes only the broadcast-flag requirement;
interfaces must still be up, loopback stays excluded, and selected addresses
must be IPv4 global unicast. Explicit binds must still belong to a qualifying
interface. On-link prefix admission and reply/inspection budgets still apply.
On `proxy`, the option is rejected without `--lan-discovery`; on `discovery`,
scan replies are already the command's purpose.

Windows discovery uses x/net with Windows control-message support from the
fork at [github.com/zmerlynn/net](https://github.com/zmerlynn/net/tree/v0.60.1-wormhole.3),
pinned to `v0.60.1-wormhole.3`. The Windows changes are submitted upstream as
[internal/socket](https://go.dev/cl/853705) and [ipv4](https://go.dev/cl/853706)
for [golang/go#7175](https://github.com/golang/go/issues/7175). The ingress,
destination and reply-source checks are the same on every OS; see
[platform validation status](status.md#platforms).

Proxy discovery uses selected cloud detail for serial, name (`ssid`), and model.
Lazy mode fetches detail without allocating a session. Replies are unicast to
the scan's original source port, sourced from the matching local LAN address
and UDP 4720. [The app uses the reply's source IP, not a JSON IP field.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#accepting-a-reply)

Requesters must be on the selected current IPv4 prefix. Network and directed
broadcast source addresses are excluded for prefixes wider than `/31`;
`/31` and `/32` have no reserved broadcast/network boundary. Destination and
requester subnet select the reply source. Ambiguous broadcasts matching several
local addresses get no reply; use an explicit bind or unicast scan.

Oversize datagrams (over 4096 bytes), truncated packets or metadata, and
malformed or non-object JSON are ignored; accepted scans follow the [request validation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#request) and the [discovery dispatcher](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#device-side-responder)
with a synthesized identity. There are four immediate
reply tokens, then one per second globally. Live interface inspections have
a separate four-immediate/one-per-second budget, including failed admissions.
Malformed scans or an exhausted reply bucket do no interface inspection.
Only successful source admission spends a reply token.

Before replying, discovery rereads ingress interface addresses. Removed or
inactive interfaces, lookup failures, and removed selected addresses get no
reply. Prefix changes for an existing selected address apply immediately.
New/replacement addresses require a restart, including wildcard mode. A reply
write has a 250 ms deadline. Reply deadline failures, write failures, and short
writes log `lan_discovery_delivery_failed`, count toward the `reply_delivery_failed`
refusal summary, and drop only that reply; discovery keeps serving. Cancellation,
read-deadline failures, and terminal receive errors still end discovery.
This bounded responder is not a flood defense.

Proxy discovery is synthesized from cloud detail; this differs from the
[recommendation to forward the real result](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#stand-in-responders).
The proxy reply includes cloud serial/name, a `product_model` synthesized from the
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

Generated license manifests retain their existing JSON layout: struct field
order, sorted map keys, indentation or line terminators, escaped HTML and
JavaScript characters, and null versus empty lists. JSON v2 uses explicit
options and zero-value omission tags to preserve those bytes.

Readers match documented field names exactly, including case; other spellings
are unknown members. All JSON readers reject duplicate names and invalid UTF-8
(including invalid Unicode escapes). Null still means absent for optional
pointer fields and zero for scalar fields decoded into fresh structs. Encoders
reject invalid UTF-8 rather than substituting replacement characters.
