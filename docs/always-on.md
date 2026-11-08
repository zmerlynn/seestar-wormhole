# Keeping it running

The proxy runs all the time on a small Linux server. It holds the telescope's
connection, and everything else (the phone, seestar_alp, SMB clients) comes and
goes through it. This page covers setting that up.

## How it behaves on its own

For unattended use, the proxy retries eligible busy responses and releases its session when stopped; see [recovery and release](reference/connection-cli.md#availability-and-busy-devices).

Alert on persistent `session_unavailable` or blocking failure records in the
[logs](reference/connection-cli.md#logs), since the process can stay running while unable to serve clients.

## Credentials for a service

Set `SEESTAR_EMAIL` and `SEESTAR_PASSWORD` in the service environment; see [signing in](../README.md#sign-in).

```sh
env_file=~/.config/seestar-wormhole.env
mkdir -p ~/.config &&
[ ! -L "$env_file" ] &&
{ [ ! -e "$env_file" ] || { [ -f "$env_file" ] && [ -O "$env_file" ]; }; } &&
(umask 077; touch "$env_file") &&
chmod 600 "$env_file"
```

After that succeeds, open the file in a text editor and add two lines:

```
SEESTAR_EMAIL=you@example.com
SEESTAR_PASSWORD=your-password
```

Use an editor rather than pasting the password into a shell command, which
would land in your shell history. If your password contains spaces,
backslashes or quotes, wrap it in single quotes (`SEESTAR_PASSWORD='...'`);
systemd would otherwise drop or reinterpret those characters. A single quote
inside the password needs systemd's escaping rules, described in
`man systemd.exec` under `EnvironmentFile=`.

Once the UI has saved your settings (it does when it connects, or when you
choose Save password to keychain), run `seestar-wormhole-cli proxy --config`, with `--state-dir PATH` for a custom UI root.
Flags override saved settings, `SEESTAR_EMAIL` overrides the saved email, and
`SEESTAR_PASSWORD` takes precedence over a read-only OS keychain lookup;
see [Use saved settings headless](reference/connection-cli.md#use-saved-settings-headless).

A macOS launchd user agent can use the keychain in the user's login session;
it may prompt once to allow access (choose Always Allow for later reads).
Windows scheduled tasks work as the same user while logged on; “Run whether
user is logged on or not” uses a batch logon, where password-backed tasks can
read persistent same-user credentials but S4U (“Do not store password”) cannot
access encrypted credentials.
Headless Linux/systemd usually has no Secret Service session, so use
`SEESTAR_PASSWORD` from the credentials file above; with `--config`, the file's
email can still come from `config.json`.

## A systemd service

Unpack the Linux archive and put `seestar-wormhole-cli` somewhere permanent:

```sh
mkdir -p ~/bin ~/.config/systemd/user
install -m 755 ./seestar-wormhole-cli ~/bin/seestar-wormhole-cli
```

Here's an example user service. Adjust the paths if you put
things elsewhere:

```ini
# ~/.config/systemd/user/seestar-wormhole.service
[Unit]
Description=seestar-wormhole-cli proxy
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=2d
StartLimitBurst=1800

[Service]
EnvironmentFile=%h/.config/seestar-wormhole.env
ExecStart=%h/bin/seestar-wormhole-cli proxy --bind 127.0.0.1 \
  --state-dir %h/.local/state/seestar-wormhole --log-level warn
KillSignal=SIGINT
TimeoutStopSec=150
Restart=on-failure
RestartSec=60
RestartPreventExitStatus=3

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now seestar-wormhole
loginctl enable-linger "$USER"
journalctl --user -u seestar-wormhole -f
```

User services normally stop when you log out. `enable-linger` keeps yours
running, which is the point.

After a [credential-rejection exit (3)](reference/connection-cli.md#exit-codes), correct the
password in the environment file (or resolve the account's authorization
restriction), then run `systemctl --user restart seestar-wormhole`.

A few choices in there:

- `--bind 127.0.0.1` keeps it to this machine. Use your LAN address if other
  computers need it. For the phone, see the [phone app guide](phone-app.md):
  it also needs `--lan-discovery` and `-p 80:80`.
- `--state-dir` stores the installation identity across restarts; see [installation identity](reference/connection-cli.md#installation-identity).
- `--log-level warn` keeps the logs quiet. At the default `info` level, every
  client connection gets a line, which adds up on an SD card.
- `KillSignal=SIGINT` and the 150-second stop timeout allow the defensive
  125-second cleanup bound (120-second dial plus five-second stop) and a margin
  to release the telescope on the way out; production stop normally takes at most five seconds.
- The two-day limit of 1,800 starts allows recovery from a day-long outage at
  `RestartSec=60`. About 30 hours of immediate failures exhausts it and leaves
  the service failed; automatic retries do not resume when the interval expires.
  After fixing the cause, run `systemctl --user reset-failed seestar-wormhole`
  followed by `systemctl --user restart seestar-wormhole`.

## Keep copying without the app

The service above keeps the connection available; it does not copy images. Run a second service for `seestar-wormhole-cli sync` to keep your image folder up to date.

`sync` never signs in or starts a connection itself:

- **Through Telescope Network**, it copies through the proxy's SMB listener,
  `127.0.0.1:4445` with the proxy's default ports and `--bind 127.0.0.1`.
  The proxy must be running with that listener on; with `-p 0:445` there is
  nothing to copy from.
- **On this network**, give the telescope's
  [address](copying.md#find-the-telescopes-address) and port 445 as
  `--endpoint`, such as `192.0.2.10:445`. No proxy or account is needed.

The examples give every flag. `--profile default` can fill the ones not
given from what the app saved for the same user on the same computer. From
the Copy settings in `copy.json` it takes the folder, copy history folder,
patterns, interval, copy once and files at once. From the connection in
`config.json` it takes the endpoint: the saved address on this network, or,
through Telescope Network, the SMB listener on the saved bind address
(`127.0.0.1` for a wildcard bind). Both files come from the app's default
settings folder, not from the proxy's `--state-dir`, and are only read.
Without saved Copy settings, `sync --profile default` exits 2. Without a
saved connection, or with the app's SMB sharing off, give `--endpoint`. If the
app's Copy is set to copy once, add `--interval 5m` so that the service keeps
checking. See [use the app's saved settings](reference/files-cli.md#use-the-apps-saved-settings).

### Sync with systemd

Use full paths: systemd doesn't expand `~`, and `%h` is your home folder.

```ini
# ~/.config/systemd/user/seestar-wormhole-sync.service
[Unit]
Description=seestar-wormhole-cli sync
After=seestar-wormhole.service
Wants=seestar-wormhole.service

[Service]
ExecStart=%h/bin/seestar-wormhole-cli sync --endpoint 127.0.0.1:4445 \
  --dest %h/Seestar
Restart=on-failure
RestartSec=60
RestartPreventExitStatus=2

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now seestar-wormhole-sync
journalctl --user -u seestar-wormhole-sync -f
```

For a telescope on this network, drop the `After=` and `Wants=` lines and
give its address as `--endpoint`. The same `loginctl enable-linger "$USER"`
as above keeps this service running after you log out.

`systemctl --user stop seestar-wormhole-sync` sends SIGTERM, which `sync`
treats as Ctrl+C, and it exits 0. During a pass it discards the files in
progress and logs `pass stopped`; between passes it exits without another
pass record. A file it was copying is copied again on the next start; see
[stopping](reference/files-cli.md#stopping). Exit 2 is a usage error, so
the service isn't restarted after one. After exit 1, for example when the
app was copying into the same folder at startup, systemd starts it again a
minute later.

### Sync in a container

Run the [proxy container](#in-a-container) first. Then run `sync` from the
same image with its own entrypoint, the folder for your images and a copy
history folder mounted:

```sh
mkdir -p ~/Seestar copy-history &&
  podman unshare chown 65532:65532 ~/Seestar copy-history
podman run -d --name seestar-wormhole-sync --network host \
  -v ~/Seestar:/images -v ./copy-history:/history \
  --entrypoint /seestar-wormhole-cli seestar-wormhole \
  sync --endpoint 127.0.0.1:4445 --dest /images --history-dir /history
```

Keep the copy history outside the images folder. `podman stop
seestar-wormhole-sync` sends SIGTERM and `sync` exits 0. This recipe has not
been run.

### Sync on Windows

Run `sync` in a second PowerShell window while the proxy from
[On Windows](#on-windows) runs, with the full path to where you unpacked the
tools:

```powershell
& "$HOME\seestar-wormhole\seestar-wormhole-cli.exe" sync --endpoint 127.0.0.1:4445 --dest "$HOME\Seestar"
```

Stop it with Ctrl+C; it exits 0.

### Sync and the app's Copy

The app's **Copy** and this service keep out of each other's way on the same
folder through its [folder lock](reference/selection-and-history.md#the-folder-lock),
`.seestar-wormhole.lock`, and, when they share the default copy history,
through its [history lock](reference/selection-and-history.md#the-history-lock):

- Starting Copy in the app for that folder while the service runs says
  **Another copy or delete is using this folder.** and copying stays off.
  With the shared default copy history that happens whenever the service
  runs; with another copy history, only while a pass is copying. A Copy that
  is already on skips a check while a pass holds the folder lock and tries
  again at the next check.
- The service exits 1 if it starts while the app's Copy is on (with another
  copy history, only while the app is copying), and with the unit above
  systemd tries again a minute later. A later pass that finds the folder
  lock held is skipped: it logs `pass skipped` with `reason=folder_busy` and
  tries again after `--interval`.

Free up space and `delete --force` still run between passes. See
[when the other side holds a lock](reference/selection-and-history.md#when-the-other-side-holds-a-lock).

## In a container

The repository includes a `Containerfile` that builds a minimal image: just
the binary and CA certificates, running as an unprivileged user. Build it and
run it like this:

```sh
podman build -t seestar-wormhole .
mkdir -p state && podman unshare chown 65532:65532 state
podman run -d --name seestar-wormhole --network host \
  --env SEESTAR_EMAIL --env SEESTAR_PASSWORD \
  -v ./state:/state \
  seestar-wormhole
```

Enter your credentials first with the prompt from the
[README](../README.md#sign-in); `--env`
with just a name passes that variable through. (Podman's `--env-file` reads
quotes literally, so the systemd file above isn't safe to reuse for a
password with quotes in it.) The image starts the proxy with a wildcard bind and state directory; explicit flags override those defaults. See [container settings](reference/connection-cli.md#containers).

For the phone, add `--lan-discovery -p 80:80` to the end, and give the
container permission to use port 80. Host networking is what lets it hear the
phone's broadcasts. The phone setup has not been run in a container; the
always-on proxy runs the binary directly. Docker takes the same flags, but it
has not been tested.

## On Windows

Use `seestar-wormhole-cli.exe` for scripts, services, and scheduled tasks. The
Windows UI executable has GUI subsystem behavior: the shell does not wait for it,
and exit codes do not reach scripts.

See [Windows coverage](status.md#platforms) and [requirements](reference/connection-cli.md#windows). In PowerShell:

```powershell
$env:SEESTAR_EMAIL = Read-Host "Seestar email"
$password = Read-Host "Seestar password" -AsSecureString
$env:SEESTAR_PASSWORD = [System.Net.NetworkCredential]::new("", $password).Password
.\seestar-wormhole-cli.exe proxy
```

Afterwards, clear the credentials with `Remove-Item Env:SEESTAR_EMAIL, Env:SEESTAR_PASSWORD`.

Stop the Windows process with Ctrl+C and use a separate SMB client for downloads; see [Windows shutdown](reference/connection-cli.md#windows) and [image copying](copying.md#windows).

## Keep it private

Keep the proxy on a trusted network because its local listeners do not authenticate clients; see [network trust](reference/connection-cli.md#proxy).

The details of busy handling, release and the installation ID are in
[the reference](reference/connection-cli.md#availability-and-busy-devices).
