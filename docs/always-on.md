# Keeping it running

The proxy runs all the time on a small Linux server. It holds the telescope's
connection, and everything else (the phone, seestar_alp, SMB clients) comes and
goes through it. This page covers setting that up.

## How it behaves on its own

For unattended use, the proxy retries eligible busy responses and releases its session when stopped; see [recovery and release](reference.md#availability-and-busy-devices).

Alert on persistent `session_unavailable` or blocking failure records in the
[logs](reference.md#logs), since the process can stay running while unable to serve clients.

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

Once the app has saved your settings (it does when it connects, or when you
choose Save password to keychain), run `seestar-wormhole proxy --config`, with `--state-dir PATH` for a custom UI root.
Flags override saved settings, `SEESTAR_EMAIL` overrides the saved email, and
`SEESTAR_PASSWORD` takes precedence over a read-only OS keychain lookup;
see [Use saved settings headless](reference.md#ui).

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

Put the binary somewhere permanent:

```sh
mkdir -p ~/bin ~/.config/systemd/user
install -m 755 ./seestar-wormhole ~/bin/seestar-wormhole
```

Here's an example user service. Adjust the paths if you put
things elsewhere:

```ini
# ~/.config/systemd/user/seestar-wormhole.service
[Unit]
Description=seestar-wormhole proxy
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=2d
StartLimitBurst=1800

[Service]
EnvironmentFile=%h/.config/seestar-wormhole.env
ExecStart=%h/bin/seestar-wormhole proxy --bind 127.0.0.1 \
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

After a [credential-rejection exit (3)](reference.md#exit-codes), correct the
password in the environment file (or resolve the account's authorization
restriction), then run `systemctl --user restart seestar-wormhole`.

A few choices in there:

- `--bind 127.0.0.1` keeps it to this machine. Use your LAN address if other
  computers need it. For the phone, see the [phone app guide](phone-app.md):
  it also needs `--lan-discovery` and `-p 80:80`.
- `--state-dir` stores the installation identity across restarts; see [installation identity](reference.md#installation-identity).
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
password with quotes in it.) The image starts the proxy with a wildcard bind and state directory; explicit flags override those defaults. See [container settings](reference.md#containers).

For the phone, add `--lan-discovery -p 80:80` to the end, and give the
container permission to use port 80. Host networking is what lets it hear the
phone's broadcasts. The phone setup has not been run in a container; the
always-on proxy runs the binary directly. Docker takes the same flags, but it
has not been tested.

## On Windows

See [Windows coverage](status.md#platforms) and [requirements](reference.md#windows). In PowerShell:

```powershell
$env:SEESTAR_EMAIL = Read-Host "Seestar email"
$password = Read-Host "Seestar password" -AsSecureString
$env:SEESTAR_PASSWORD = [System.Net.NetworkCredential]::new("", $password).Password
.\seestar-wormhole.exe proxy
```

Afterwards, clear the credentials with `Remove-Item Env:SEESTAR_EMAIL, Env:SEESTAR_PASSWORD`.

Stop the Windows process with Ctrl+C and use a separate SMB client for downloads; see [Windows shutdown](reference.md#windows) and [image copying](copying.md#windows).

## Keep it private

Keep the proxy on a trusted network because its local listeners do not authenticate clients; see [network trust](reference.md#proxy).

The details of busy handling, release and the installation ID are in
[the reference](reference.md#availability-and-busy-devices).
