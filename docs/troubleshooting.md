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
the telescope, and it stops trying to reconnect until you restart it.
Waiting won't fix it. Stop the proxy, wait for it to exit, give the
cloud a few minutes, then start it again. File an issue if it keeps
happening.

A `site_protected` event means the proxy dropped a site or time setter, or removed those fields from `set_setting`; see [site protection](reference.md#site-protection).

A `cloud_license_missing` event means allocation returned no SDK license key; retries stop. See [the log reference](reference.md#logs).

For `iotc_rid_history_exhausted`, restart the process after it exits; the reserved identifier budget lasts for the process lifetime. See [logs](reference.md#logs).

**The phone app can't find the telescope.** See
[the phone app guide](phone-app.md#if-the-telescope-doesnt-show-up). Most of
the time it's the firewall or a Wi-Fi network that keeps devices apart.

If the album is empty, start the proxy with `-p 80:80`; see [HTTP port mappings](reference.md#port-mappings).

To correct the stored site, connect the app directly to the device and set it there; see [site protection](reference.md#site-protection).

**Status replies are slow.** A large download is probably sharing the
connection. See [downloading while you observe](pulling.md#downloading-while-you-observe).

A `local_closed` poller event is often a client that closed after one reply; device-side failures still warrant investigation. See [logs](reference.md#logs).

**Sign-in fails.** Check `SEESTAR_EMAIL` and `SEESTAR_PASSWORD` by signing in
to the phone app with the same values. Type them at the `read` prompt from the
[README](../README.md#before-you-start-your-account-and-your-site) rather than
pasting them into a command line.

## pull

Start with `reason` and `cleanup_reason` in the pull summary; finished files keep receipts, while a source or layout mismatch needs its original binding or a new destination. See [recovery details](reference.md#summary-and-progress).

## Filing an issue

Include the version (`seestar-wormhole --version`), your platform, the
telescope model and firmware, and the log lines around the problem. The logs
leave out your credentials, and errors leave out file paths, but some lines
include a partly masked serial number or your network addresses, and debug
dry-run output lists file names. Read them over and remove anything you'd
rather keep before posting.

A key log and packet capture can expose session contents; keep both private and never attach them to an issue. See [sensitive capture handling](reference.md#sensitive-keylog).
