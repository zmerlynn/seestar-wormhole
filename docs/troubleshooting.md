# Troubleshooting

## The proxy

When the proxy exits with failure, inspect the final `proxy failed` record and its `reason`; see [failure diagnosis](reference.md#proxy-final-failure).

**macOS won't run it.** It came from the internet, so macOS quarantines it.
Allow it under System Settings → Privacy & Security, or run
`xattr -d com.apple.quarantine seestar-wormhole`.

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
[README](../README.md#before-you-start-your-account-and-your-site) rather than
pasting them into a command line.

## Filing an issue

Include the version (`seestar-wormhole --version`), your platform, the
telescope model and firmware, and the log lines around the problem. The logs
leave out your credentials, and errors leave out file paths, but some lines
include a partly masked serial number or your network addresses. Read them over and remove anything you'd
rather keep before posting.

## The browser didn't open

Run `seestar-wormhole ui` from a terminal. If the default browser opener fails,
the command prints a private URL; copy the whole URL, including its fragment,
into a browser on that computer. Keep it private. Launching again with the same
state directory reopens the running page. If another instance is still starting,
wait briefly and try again. Ctrl+C releases the session and exits.
See [browser controls](reference.md#ui).

## The OS keychain is unavailable

A locked keychain, missing desktop session, or unavailable Secret Service can
prevent password storage. The page says when it keeps the password in memory;
there is no plaintext fallback. Enter it again after restarting, or unlock your
normal OS keychain and retry Save settings with Remember password selected.
Forget clears the memory copy; if the OS deletion fails, unlock the provider
and retry Forget for the same email. Saved settings never contain the password.
