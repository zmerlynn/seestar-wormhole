# Troubleshooting

## The proxy

When the proxy exits with failure, inspect the final `proxy failed` record and its `reason`; see [failure diagnosis](reference.md#proxy-final-failure).

**macOS won't run it.** A notarized download opens after macOS's one-time
"downloaded from the Internet" prompt. If a copy still refuses to run (for
example, the CLI copied somewhere unusual), allow it under System Settings →
Privacy & Security, or run `xattr -d com.apple.quarantine seestar-wormhole-cli`
on that copy.

**It keeps logging `session_unavailable`.** Look at the `reason` in that line.
`busy` means the [cloud reports remote allocation as busy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#observed-single-session-enforcement).
Another remote session may still be active, such as the app or another proxy.
Close the app's remote session or stop the other proxy, and this one keeps
retrying until it gets in. Stop the proxy before switching to the app's
remote mode. If you just killed a proxy outright, the cloud may keep the
telescope marked busy for a minute or more. A reason like `release_failed` or
`ownership_uncertain` is different: the proxy couldn't confirm it let go of
the telescope. After cleanup finishes, it waits 60 seconds and tries a
fresh allocation automatically. In lazy mode, it waits for client demand.
Later busy answers keep retrying under the [availability policy](reference.md#availability-and-busy-devices).
The reminder keeps the original reason until recovery succeeds; look for
`session_state` transitions through `backoff` and `dialing`. File an issue if
it keeps happening.

A `site_protected` event means the proxy dropped a site or time setter; see [site protection](reference.md#site-protection).

A `cloud_license_missing` event means allocation returned no SDK license key; retries stop. See [the log reference](reference.md#logs).

For `iotc_rid_history_exhausted`, stop the process normally, wait for session release and exit, then restart it; retries in the same process cannot restore the budget. See [logs](reference.md#logs).

**The phone app can't find the telescope.** See
[the phone app guide](phone-app.md#if-the-telescope-doesnt-show-up). Most of
the time it's the firewall or a Wi-Fi network that keeps devices apart.

A `lan_discovery_delivery_failed` record with a `reply_*` rule means one discovery reply couldn't be sent, usually because the proxy's LAN address just changed. Discovery keeps running. If it repeats, check that the bind address is still on the interface, or restart the proxy to pick up a new address. See [LAN discovery](reference.md#lan-discovery).

If the album is empty, start the proxy with `-p 80:80`; see [HTTP port mappings](reference.md#port-mappings).

To correct the stored site, connect the app directly to the device and set it there; see [site protection](reference.md#site-protection).

**Status replies are slow.** A large download is probably sharing the
connection. See [downloading while you observe](copying.md).

A `local_closed` poller event is often a client that closed after one reply; device-side failures still warrant investigation. See [logs](reference.md#logs).

**Sign-in fails.** Check `SEESTAR_EMAIL` and `SEESTAR_PASSWORD` by signing in
to the phone app with the same values. Type them at the `read` prompt from the
[README](../README.md#sign-in) rather than
pasting them into a command line.

## Filing an issue

Include the version (`seestar-wormhole-cli --version`), your platform, the
telescope model and firmware, and the log lines around the problem. The logs
leave out your credentials, and errors leave out file paths, but some lines
include a partly masked serial number or your network addresses. Read them over and remove anything you'd
rather keep before posting.

## The native window didn't open

Launch `seestar-wormhole` from the unpacked archive (or the Mac app), and check any startup error. `seestar-wormhole-cli` has no UI command or browser fallback. On Linux, see [native window prerequisites](#linux-native-window) below. If a headless host reports a loader error about `libgtk` or WebKit, use `seestar-wormhole-cli` from the same archive and update the service's executable path.
Launching the UI again with the same state directory brings its window forward.
See [Using the app](ui.md).

## Saved settings prevent startup

The native app shows **Could not open seestar-wormhole** with the refused
settings file's location. Check this file, then reopen seestar-wormhole.
The app leaves the file untouched. Default locations are:

- macOS: `~/Library/Application Support/seestar-wormhole/config.json`
- Linux: `$XDG_CONFIG_HOME/seestar-wormhole/config.json`, or
  `~/.config/seestar-wormhole/config.json` when that variable is unset.
- Windows: `%AppData%\seestar-wormhole\config.json`

## The OS keychain is unavailable

A locked keychain, missing desktop session, or unavailable Secret Service can
prevent password storage. The page says when it keeps the password in memory;
there is no plaintext fallback. Enter it again after restarting, or unlock your
normal OS keychain and choose Save password to keychain. Remove saved password
clears the memory copy and requests keychain deletion. If deletion fails, unlock
the keychain and choose Retry removal. Saved settings never contain the password.

### Linux native window

The GUI build requires GTK 4, WebKitGTK 6.0, and bubblewrap (on Ubuntu:
`libgtk-4-1`, `libwebkitgtk-6.0-4`, and `bubblewrap`). WebKit's sandbox needs
permission to create unprivileged user namespaces. Ubuntu 24.04's AppArmor
restriction can deny bubblewrap's uid-map setup even with the distro package
installed. The app checks bubblewrap before starting WebKit and reports
“WebKit sandbox unavailable” if this prerequisite fails.

Use a distribution-supported AppArmor profile that permits the sandbox for
this application, or ask the administrator to supply one. The product never
disables the WebKit sandbox. A global user-namespace policy relaxation is
not the recommended setup. Use `seestar-wormhole-cli` for headless operation.
CI probes the restricted policy before using a disposable
runner policy relaxation for its native rendering tests.
