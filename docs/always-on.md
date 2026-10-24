# Keeping it running

The proxy runs all the time on a small Linux server. It holds the telescope's
connection, and everything else (the phone, seestar_alp, SMB clients) comes and
goes through it. This page covers setting that up.

## How it behaves on its own

For unattended use, the proxy retries eligible busy responses and releases its session when stopped; see [recovery and release](reference.md#availability-and-busy-devices).

Alert on persistent `session_unavailable` or blocking failure records in the
[logs](reference.md#logs), since the process can stay running while unable to serve clients.

## Credentials for a service

Set `SEESTAR_EMAIL` and `SEESTAR_PASSWORD` in the service environment; see [account setup](../README.md#before-you-start-your-account-and-your-site).

```sh
(
  set -e
  mkdir -p ~/.config
  env_file=~/.config/seestar-wormhole.env
  [ -e "$env_file" ] || [ -L "$env_file" ] || (umask 077; set -C; : > "$env_file")
  [ ! -L "$env_file" ] && [ -f "$env_file" ] && [ -O "$env_file" ] ||
    { echo "Refusing unsafe environment file" >&2; exit 1; }
  chmod 600 "$env_file"
)
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

[Service]
EnvironmentFile=%h/.config/seestar-wormhole.env
ExecStart=%h/bin/seestar-wormhole proxy --bind 127.0.0.1 \
  --state-dir %h/.local/state/seestar-wormhole --log-level warn
KillSignal=SIGINT
TimeoutStopSec=30
Restart=on-failure
RestartSec=60

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

A few choices in there:

- `--bind 127.0.0.1` keeps it to this machine. Use your LAN address if other
  computers need it. For the phone, see the [phone app guide](phone-app.md):
  it also needs `--lan-discovery` and `-p 80:80`.
- `--state-dir` stores the installation identity across restarts; see [installation identity](reference.md#installation-identity).
- `--log-level warn` keeps the logs quiet. At the default `info` level, every
  client connection gets a line, which adds up on an SD card.
- `KillSignal=SIGINT` and the 30-second stop timeout give the proxy time to
  release the telescope on the way out.

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
[README](../README.md#before-you-start-your-account-and-your-site); `--env`
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
