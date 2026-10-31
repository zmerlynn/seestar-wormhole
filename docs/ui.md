# Using the UI

seestar-wormhole connects apps on your computer and phone to your remote Seestar.
They share one connection to the telescope.

![Choose who can connect, then enter your Seestar account details](images/gui-setup.png)

## Setting up

Under **Use with**, choose who can connect:

- **Phone on this network** is selected the first time. It listens on all
  networks, lets the Seestar app find your telescope, and serves image downloads
  on local port 80. Keep your phone on the same network as this computer. Use a
  trusted network: other devices on it can control your Seestar.
- **This computer only** accepts apps on this computer only. Discovery is off,
  and image downloads use local port 8080.
- **Custom** uses your own settings under **Advanced**. Changing any of them
  selects **Custom**; picking another setup replaces them.

Enter your Seestar account email and password. Nothing connects until you
choose **Connect**, and next time your choices are already filled in.

Your password stays in memory until you quit. To skip typing it next time,
choose **Save password to keychain**; the field then shows **Saved in
keychain**. Your password is never saved in plain text.

To change a saved password, choose **Remove saved password** and type the new
one. If removal fails, unlock your keychain and choose **Retry removal**. See
[keychain troubleshooting](troubleshooting.md#the-os-keychain-is-unavailable).

## Connecting

Choose **Connect**. While a connection is running, the button becomes **Cancel**
or **Stop**. The status tells you what's happening:

| Message | What to do |
|---|---|
| **Connected to your Seestar** | Start using your apps. |
| **Waiting for an app to connect** | Connect an app to a forwarded port. The session starts when one does. |
| **Seestar is occupied. Retrying automatically.** | Disconnect any other remote session, or wait for it to let go. |
| **Connection interrupted. Retrying automatically.** | Wait; seestar-wormhole reconnects on its own. |
| **Waiting before retrying the connection…** | Wait for cleanup and the retry delay. |
| **Stopped. Seestar may still be releasing the session.** | Give it time before using another remote session. |
| **Session release could not be confirmed. If Seestar is occupied, wait and try Connect again.** | Wait, then choose **Connect** again. |
| Could not sign in. Check your Seestar account email and password, then connect again. | Fix your account details and choose **Connect**. |

If a setting is wrong, its field shows what to fix.

Your settings are saved once a connection succeeds. If saving fails, the
connection keeps running and **Retry saving settings** appears.

If the window loses the controls, it shows the last known state and warns that
the proxy may still be running. Reopen seestar-wormhole to reconnect.

## While connected

The setup fields are replaced by a summary of listening, discovery, and
connection timing. Open **Ports forwarded to your Seestar** to see the ports in
use: **Local port** is on this computer, **Seestar port** is on the telescope,
and **Purpose** names the service. You can't edit them while connected.

When discovery is on, **Using the Seestar app** tells you to pick your
telescope from the **LAN** tab of the Seestar app. If it doesn't appear, see
the [phone app guide](phone-app.md). **Copying files** shows
the local SMB port; see [copying images](copying.md).

Keep seestar-wormhole open while you use the telescope.

## Stopping and quitting

**Stop** releases the telescope and leaves the window ready to connect again.
**Quit app** stops the connection and exits. Closing the window does the same
and waits for the release. Logging out or shutting down also stops the
connection first, as far as your system allows.

Opening seestar-wormhole again with the same state directory brings the
existing window forward. If `seestar-wormhole-cli proxy` is already using the
same ports, stop it first.

## Advanced

### Telescope selection

If your account has one Seestar, leave **Serial number (optional)** and
**Model (optional)** empty. If it has several, enter the serial and model
exactly as your account shows them. See [device selection](reference.md#device-selection).

### Timing and idle

Under **Connection timing**:

- **Keep connected** starts the session when you connect and holds it until you
  stop.
- **Connect when an app needs Seestar** waits for an app.
  **Release after apps disconnect (minutes)** sets how long to wait after the
  last app leaves. The next app starts the session again.

### Listening and discovery

Under **Listen on**, choose **All networks**, **This computer only**, or
**Other…**. For **Other…**, enter this computer's IPv4 address in **Local
network address**.

**Let the Seestar app find your Seestar** answers discovery requests on your
network. It needs **All networks** or a local network IPv4 address. See
[LAN discovery](reference.md#lan-discovery) for network requirements and limits.

### Services and ports

The **Services** table has ten fixed rows. Leave a **Local port** empty to turn
that service off. Each enabled local port must be unique and between 1 and
65535. The phone setup maps local port 80 to Seestar port 80; the computer-only
setup uses local port 8080 instead. SMB uses local port 4445 for Seestar port
445. For other mappings, use the [command line](reference.md#port-mappings).

## Details and logs

**Details and logs** shows your connection settings as JSON, recent events in
local time, error codes, and the private log's location.

**Copy diagnostic summary** copies one JSON object with the program name,
version, connection settings, and this launch's events in UTC. It leaves out
addresses, paths, credentials, tokens, account and device identifiers, and
payloads.

The private logs use fixed messages and categories, UTC timestamps, and
classified failure phases and reasons, with no raw protocol traces.

If seestar-wormhole fails to start, your system shows the error. On Windows,
the error says when you need the WebView2 Runtime.

## Running without the window

Once the UI has saved your settings, `seestar-wormhole-cli proxy --config` can
use them without a window. See [Use saved settings headless](reference.md#use-saved-settings-headless)
for password sources, overrides, and service setup.

## Details for admins

### Where settings live

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
| `InterProcessKV/device_uuid` | Your [installation identity](reference.md#installation-identity), shared with `proxy --state-dir PATH`. |
| `gui.log`, `gui.log.1` | UI events as JSON lines, up to 64 KiB each, with one rotation. |
| `ui.lock` | Keeps one UI running per state directory. |
| `ui-runtime.json` | Private control port and token; removed on normal exit. |
| `webview-<random>/` | Windows only: a fresh private engine profile for each launch; see [engine data](#engine-data). |
| `ui-open.html` | Private browser page that grants access to the controls; removed on normal exit. |

### config.json version 1

`version` is required and must be `1`. Missing members take these defaults:

| Member | Default and meaning |
|---|---|
| `bind` | IPv4 loopback, for this computer only. |
| `discovery` | `false` |
| `idle` | `"0"`; a nonnegative Go duration string, such as `"2m"`. |
| `serial`, `model`, `email` | Empty strings. |
| `ports` | `[]`, using the standard service mappings. |
| `remember_password` | `false` |

These defaults differ from the UI's first-run phone setup.

Each `ports` override has lowercase numeric `local` and `device` members, from
0 to 65535. A `local` of 0 turns the service off; `device` must name a standard
service.

Unknown members survive a save. Reads are limited to 64 KiB and never rewrite
the file. A missing or unsupported version, including a newer one, is refused
and left untouched. A failed load never overwrites existing state.

### File permissions

Saves are atomic. On Unix, the directory must be `0700` and its files `0600`;
unsafe permissions or links are refused. Windows uses the same protected current-user/SYSTEM ACLs as the
[installation identity](reference.md#windows). Use trusted parent
directories.

The CLI keeps its default identity in memory only. To share the UI's identity,
pass the same state root explicitly, or use `proxy --config`.

### The control page

The controls use a separate loopback listener on an automatic port, even when forwarding listens on other networks. Access takes a random
256-bit token: it starts in the URL fragment and goes in a header with every API
request. Treat that private URL as access to the controls.

To keep other websites out, the page uses exact Host checks, Origin checks on
JSON POST writes, request limits, blocked framing, a restrictive content
policy, and no-store/no-referrer headers. Other programs running as your user
are still trusted.

A browser opener gets only the path to `ui-open.html`, which redirects to the
controls, so the token stays out of its arguments. If opening fails,
seestar-wormhole prints the private URL; keep that output private. A crash can
leave `ui-open.html` behind; the next browser launch replaces it.

### Window limits

Dropping a file or link onto the window can replace the controls with another
page, and an engine popup can open another view or browser window. A page from
another origin can't read the control token or make authenticated control
requests, and the Wails command bridge is blocked, but nothing stops the
navigation or popups themselves. Reopen seestar-wormhole to bring the controls
back in the existing window. Right-click menus are the engine's defaults.

For Linux prerequisites, see [Linux native window](troubleshooting.md#linux-native-window).

### Engine data

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
