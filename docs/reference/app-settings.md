# App settings and internals

Where the app (`seestar-wormhole`) keeps its settings, the formats of its
settings files, and how its control page and window are protected. For what
the app's tabs do, see [Using the UI](../ui.md).

## Where settings live

The default state directory is:

| System | Directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/seestar-wormhole`, or `$HOME/.config/seestar-wormhole` |
| macOS | `$HOME/Library/Application Support/seestar-wormhole` |
| Windows | `%APPDATA%\seestar-wormhole` |

To use another existing installation root, run
`seestar-wormhole ui --state-dir PATH`. This doesn't move or copy its identity.

| File | Purpose |
|---|---|
| `config.json` | Settings except the password; see [the format](#configjson-version-1). |
| `copy.json` | Copy settings; see [the format](#copyjson-version-1). |
| `InterProcessKV/device_uuid` | Your [installation identity](connection-cli.md#installation-identity), shared with `proxy --state-dir PATH`. |
| `gui.log`, `gui.log.1` | UI events as JSON lines, up to 64 KiB each, with one rotation. |
| `ui.lock` | Keeps one UI running per state directory. |
| `ui-runtime.json` | Private control port and token; removed on normal exit. |
| `webview-<random>/` | Windows only: a fresh private engine profile for each launch; see [engine data](#engine-data). |
| `ui-open.html` | Private browser page that grants access to the controls; removed on normal exit. |

The copy history and its locks are not in the state directory. By default they
are in the file commands' default [history location](selection-and-history.md#location),
`seestar-copy` beside `seestar-wormhole` in the same system folder, whatever
`--state-dir` says. See [selection and copy history](selection-and-history.md)
for how the app and `seestar-wormhole-cli` share them.

## config.json version 1

`version` is required and must be `1`. Missing members take these defaults:

| Member | Default and meaning |
|---|---|
| `bind` | IPv4 loopback, for this computer only. |
| `discovery` | `false` |
| `idle` | `"0"`; a nonnegative Go duration string, such as `"2m"`. |
| `serial`, `model`, `email` | Empty strings. |
| `ports` | `[]`, using the standard service mappings. |
| `remember_password` | `false` |
| `route` | Empty, meaning **Remotely through Telescope Network**. `"lan"` means the copier reaches the telescope on this network at `lan_address`; any other value means Telescope Network. |
| `lan_address` | Empty. The telescope's address on this network as `host:port`, saved by **On this network** > **Save**. |
| `lan_serial` | Empty. The serial number a scan of this network saw at `lan_address` when it was saved, or that a scan or Test heard there when you chose **Use the address's folder** for it. Copy, Browse and Free up space, and `--profile default` on the command line, scan for it when `lan_scan` says and use the address that answers with it, `lan_address` first; the app saves a new address here, unless a Save came since. An address where another serial answers is never used; when no reply carries it, `lan_address` is tried. Absent or empty, the saved address is used as it is, without a scan. It is never shown in the app or logged. |
| `lan_scan` | Empty, meaning `"start"`. When a connect scans this network for `lan_serial`, chosen under **On this network** > **Scan for the saved telescope**: `"start"` once per run (starting copying, opening Browse, each Copy preview and each Free up space check, and each command-line command), and again only when a connect fails; `"connect"` before every connect; `"off"` never, and the app then sends no scan request it wasn't asked for. Any other value is treated as `"start"` and kept until a new choice is saved. `--scan` overrides it for one command. It can't be changed while connected through Telescope Network. |
| `route_established` | `false`. `true` once a connection through Telescope Network is ready or an address on this network passed Test and was saved; it opens **Copy**, **Browse** and **Free up space** at launch. A password save never sets it. |

These defaults differ from the UI's first-run phone setup.

Each `ports` override has lowercase numeric `local` and `device` members, from
0 to 65535. A `local` of 0 turns the service off; a `device` that isn't a
standard service adds a port. An older app refuses a file with added ports.

A newer app, telescope or account could save a value this version refuses.
A bad value in one of these settings doesn't refuse the file. It loads as a
fallback, and the next save writes the fallback:

| Bad value | Loads as |
|---|---|
| `discovery` `true` with a `bind` discovery can't use | `false` |
| `serial` or `email` that isn't valid | Empty, as if none were saved |

Each fallback logs one warning, `settings_fallback`, naming the file, the
setting and the fallback, never the saved value: the app's in `gui.log`,
`proxy --config`'s in its log. Entering a bad value in the app is still an
error on that field.

Unknown members survive a save. Reads are limited to 64 KiB and never rewrite
the file. Any other bad value, such as an `idle`, `bind`, `model` or port the
app refuses, a missing or unsupported version, including a newer one, a member
of the wrong type, or a file that doesn't parse is refused and left untouched.
A failed load never overwrites existing state.

## copy.json version 1

`version` is required and must be `1`. Missing members take these defaults,
and members this version doesn't know are kept when it saves. The file is
written when you save Copy settings, preview Free up space with changed
options, or accept an address's folder for a telescope. After an upgrade,
the saved telescope's folder is also recorded before its first Copy,
preview, Free up space check, history clear, or connection change. Loading
alone never writes the file.

| Member | Default and meaning |
|---|---|
| `folder` | `Seestar` in your home folder. The first telescope's folder, which an older app uses for every telescope; see `telescopes`. |
| `telescopes` | Absent. Each telescope's folder, keyed `serial:<serial>` for a telescope known by its serial number, through either route, or `address:<host:port>` for an address on this network saved without one, as the address is saved. Each entry is `{"folder": "<absolute path>"}`; members this version doesn't know are kept. Until an entry has a folder, every telescope's folder is `folder`, and the first telescope Copy uses, the one saved on the Connection tab, is written in with it; after that a telescope without an entry has no folder and is asked for one. Two entries never hold the same folder or one inside another, by their text or on disk through symbolic links. |
| `history_dir` | `""`: the default [history location](selection-and-history.md#location), as `--history-dir`'s default. Otherwise **Copy history folder**, an absolute path outside `folder`. A relative path or a value that isn't a string refuses the file. |
| `interval_minutes` | `5` |
| `once` | `false`; `true` when **Check for new files every** is off: Start copying checks once, then stops. |
| `excludes`, `includes` | `[]`; **Leave out** and **Only copy**, one pattern each. |
| `skip_built_in` | `true`; Copy only. Free up space always leaves out the built-in patterns. |
| `start_copying` | `false` |
| `files_at_once` | `4`; **Files at once**, from `1` to `4`. A value outside that range loads as the nearest of them, and a `settings_fallback` warning names the setting and the fallback, never the saved value, in `gui.log` or the `--profile default` command's log. |
| `older_than_days` | `7` |
| `trust_state` | `false` |
| `free_excludes`, `free_includes` | `[]`; **Keep on the telescope** and **Only free up**, one pattern each. |

Older versions keep `history_dir` when saving but use the default history
folder. After a downgrade, files moved out of the images folder can be copied
again, and Free up space cannot use the chosen folder's history to prove those
copies safe.

The telescope's address is not in it; it comes from the route in `config.json`.
`telescopes` names a telescope only by its key, which also chooses the
folder `--profile default` fills; see
[use the app's saved settings](files-cli.md#use-the-apps-saved-settings).

`telescopes` needs no new version: an older app reads `folder` and keeps
`telescopes` when it saves, so after a downgrade every telescope copies into
the first telescope's folder until the newer app is back.

A copy.json this version can't read, such as one with another version, a
member of the wrong type or a relative `history_dir`, doesn't stop the app.
The Connection tab and the proxy work, but **Copy**, **Browse** and **Free up
space** stay off for that run, a note under the tabs names the file, and
Activity records **Copy settings couldn't be loaded safely; Copy, Browse and
Free up space are off.** The app never writes the file while it is refused. Fix
or remove it, then reopen the app; a removed file brings back the default Copy
settings. A refused `config.json` still stops the app.

## File permissions

Saves are atomic. On Unix, the directory must be `0700` and its files `0600`;
unsafe permissions or links are refused. Windows uses the same protected current-user/SYSTEM ACLs as the
[installation identity](connection-cli.md#windows). Use trusted parent
directories.

The CLI keeps its default identity in memory only. To share the UI's identity,
pass the same state root explicitly, or use `proxy --config`.

## The control page

The controls use a separate loopback listener on an automatic port, even when forwarding listens on other networks. Access takes a random
256-bit token: it starts in the URL fragment and goes in a header with every API
request. Treat that private URL as access to the controls.

To keep other websites out, the page uses exact Host checks, Origin checks on
JSON POST writes, request limits, framing by the same page only, a restrictive
content policy, and no-store/no-referrer headers. The copy API under
`/copy/api/` takes the same token and checks; Browse, under `/browse/`, is
served for a frame on the page. Other programs running as your user
are still trusted.

A browser opener gets only the path to `ui-open.html`, which redirects to the
controls, so the token stays out of its arguments. If opening fails,
seestar-wormhole prints the private URL; keep that output private. A crash can
leave `ui-open.html` behind; the next browser launch replaces it.

## Window limits

Dropping a file or link onto the window can replace the controls with another
page, and an engine popup can open another view or browser window. A page from
another origin can't read the control token or make authenticated control
requests, and the Wails command bridge is blocked, but nothing stops the
navigation or popups themselves. Reopen seestar-wormhole to bring the controls
back in the existing window. Right-click menus are the engine's defaults.

For Linux prerequisites, see [Linux native window](../troubleshooting.md#linux-native-window).

## Engine data

On Windows, each launch gets a new `webview-<random>/` profile. After the
window closes, seestar-wormhole waits up to two seconds to remove it. If files
are still locked, it prints a warning but exits normally. Later launches try
again, including for the older `webview/` directory, and locked profiles don't
block startup. Leftover cache and session files stay in the private state directory until
they unlock or you remove them after exit. Profiles with unsafe permissions or
links are left for you to remove by hand.

On macOS (WKWebView) and Linux (WebKitGTK), the engines use their default
persistent stores outside the state directory. These aren't cleared on exit.
The page uses sessionStorage to authorize reloads and sets no cookies or
localStorage. No-store headers don't guarantee that an engine writes no session
or cache data, and what each engine keeps on disk hasn't been checked on every
platform.
