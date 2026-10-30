# Using the phone app from home

The trick here is that the phone app has [two ways to find a telescope](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#address-entry-saved-devices-and-lan-versus-remote): over
the cloud (remote mode) and on the local Wi-Fi (the LAN list). seestar-wormhole
holds the cloud connection itself and then shows up in the LAN list, so the app
treats a telescope thousands of miles away as if it were in the next room.

Phone use requires LAN discovery; the tested phone platforms and paths are listed in [phone coverage](status.md#phone-app).

## What you need

- A computer on the same Wi-Fi network as your phone. The tested computer was
  a Mac. Linux or a Raspberry Pi should work the same way, though neither has
  been tested. See [platform coverage](status.md#platforms) for Windows.
- Your Seestar account email and password.
- A correct site stored on the telescope. The device keeps that site and its
  own clock. To change the site, connect the app to the device without the
  proxy; the [README](../README.md#before-you-start-your-account-and-your-site)
  explains why.

## Start the proxy

In the GUI, leave **Use with → Phone on this network** selected, enter your
Seestar account email and password, and choose **Connect**. This enables LAN
discovery and computer port 80 for album/image access. Use only a trusted
network: other devices on it can control the telescope through this computer.
A successful connection saves non-secret settings. Saving the password to
keychain is a separate, optional action. Existing saved setups keep their
network exposure; choose Phone explicitly if upgrading from computer-only.

Advanced shows a fixed port table; leave a Local cell empty to disable that service. The running page shows the effective ports and Seestar app steps. Keep the computer and phone on the same
network and open the Seestar app.
Keep seestar-wormhole running. Stop and wait for release before using the app's
remote mode. Details and logs contains safe diagnostic history. If the GUI
reports a phone HTTP permission failure, see [port 80 and permissions](#port-80-and-permissions).

For the CLI, start with discovery enabled:

```sh
./seestar-wormhole proxy --bind 0.0.0.0 --lan-discovery -p 80:80
```

The command enables LAN discovery and maps local port 80 to the device’s HTTP port; see [discovery and HTTP mappings](reference.md#port-mappings).

Leave it running. If your account has more than one telescope, add
`--serial <serial>`; see [the reference](reference.md#device-selection).

## Connect the app

1. On the phone, give the Seestar app **Local Network** permission if it
   asks (on iOS: Settings → Privacy & Security → Local Network).
2. Open the app and look at the **LAN** device list. Your telescope should
   appear under its usual name.
3. Pick it. From here on it behaves like any local connection.

Stop the proxy and wait for it to exit before switching the phone app to remote mode; see [handing the session back](reference.md#availability-and-busy-devices).

## If the telescope doesn't show up

- **Firewall.** On a Mac, allow incoming connections when macOS asks, or add
  the binary under System Settings → Network → Firewall → Options. On Linux,
  open UDP 4720 and the TCP ports you've mapped.
- **Guest or "isolated" Wi-Fi.** Many routers stop devices on the same network
  from talking to each other. The phone has to be able to reach the computer.
- **The proxy isn't ready yet.** It has to finish connecting to the telescope
  before the app can use it. Look for a `session_phase_complete` line with
  `"phase":"ready"` in its output. (With `--idle`, it only connects once the
  app picks it, so this doesn't apply.)
Discovery replies use the proxy’s current local address; after an address change, restart the proxy and reconnect the app. See [LAN discovery](reference.md#lan-discovery).
For discovery over VPN or virtual adapters, see `--lan-discovery-all-interfaces` in the [reference](reference.md#lan-discovery).
- **It's running in a container or VM.** It needs to see the phone's broadcast
  packets directly, so use host networking or run the binary natively.

## Port 80 and permissions

Port 80 is a "privileged" port. On a Mac, `--bind 0.0.0.0` can use it without
admin rights, but binding a specific address like `192.168.1.20` may fail with
a permission error. On Linux, you'll need `sudo` or the
`cap_net_bind_service` capability. Plain `sudo` drops your exported
credentials; `sudo --preserve-env=SEESTAR_EMAIL,SEESTAR_PASSWORD` keeps them.

Anyone who can reach the listener can use it, so keep the proxy on a trusted network; see [network trust](reference.md#proxy).

## Connect only when needed

Add `--idle 2m` to allocate only when a client connects and release after two idle minutes; see [lazy connection behavior](reference.md#availability-and-busy-devices).

## Discovery at home with a primary on another LAN

See [tested coverage](status.md#phone-app) for this chained setup. Keep the
single primary proxy and its credentials on the remote Linux host. On the
home Linux machine, use SSH for TCP and `discovery` for the phone's local
scans. Use a stable home LAN IPv4 address reachable from the phone; all home
listeners and discovery replies must use that address.

On the primary, keep the existing instance and mappings used by other
clients; a default example is:

```sh
seestar-wormhole proxy --bind 127.0.0.1 --state-dir '<primary-private-state-dir>'
```

Run that instance as the [primary systemd user service](always-on.md#a-systemd-service),
with credentials only there. It needs neither `--lan-discovery` nor a
privileged HTTP listener. Do not start a second proxy alongside it.
For a system unit, keep the primary recipe's restart limits,
`RestartPreventExitStatus=3`, and `TimeoutStopSec=150`.

On the home machine, run:

```sh
ssh -N -T -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -L '<home-lan-ip>:4700:127.0.0.1:4700' \
  -L '<home-lan-ip>:4800:127.0.0.1:4800' \
  -L '<home-lan-ip>:4804:127.0.0.1:4804' \
  -L '<home-lan-ip>:4801:127.0.0.1:4801' \
  -L '<home-lan-ip>:4554:127.0.0.1:4554' \
  -L '<home-lan-ip>:80:127.0.0.1:8080' \
  '<ssh-user>@<primary-host>'
```

The right-hand ports must match the primary's actual local listeners. Add
`-L '<home-lan-ip>:4555:127.0.0.1:4555'` for supported wide video if needed.
Updater forwards on 4350 and 4361 are optional; add them only for intentional
firmware updating. Discovery UDP 4720 stays on the home LAN. This setup
does not forward RTSP-negotiated UDP video.

Home TCP 80 must accept the phone's portless HTTP URLs. An administrator can
set Linux `net.ipv4.ip_unprivileged_port_start=80` in the home machine's
network namespace, allowing unprivileged processes to bind ports 80 and
above. This affects every process in that namespace. Alternatively, have the
administrator redirect home TCP 80 to an unprivileged listener such as
18080, and change only the HTTP forward to
`-L '<home-lan-ip>:18080:127.0.0.1:8080'`. Keep the redirect restricted to
the selected home LAN address/interface. Neither option requires running
SSH as root or adding broad capabilities to the SSH executable.

In another terminal on the home machine:

```sh
seestar-wormhole discovery --bind '<home-lan-ip>' \
  --advertise-serial '<serial>' --advertise-model '<model-label>' \
  --advertise-name '<device-name>'
```

Use the already verified serial and exact model label from the primary's
configuration (`--serial` and `--model`, if explicitly configured). The
app's device list provides the display name; omitting `--advertise-name`
uses the model instead. There is no existing CLI export of all three
values for an automatically selected device. Logs mask serials, so they
are not a provisioning source; obtain a verified configuration before using
this recipe. Values in flags are visible in shell history and process
arguments. See [discovery flags and validation](reference.md#discovery).

Allow home UDP 4720 and only the selected TCP forwards from the trusted
phone LAN. SSH protects home-to-primary traffic; phone-to-home traffic
remains plaintext and unauthenticated. Local-network permissions, AP client
isolation and duplicate-serial warnings from the earlier setup still apply.
Select the app's **LAN** entry. An advertised row is not proof that the
SSH transport, primary session or telescope is ready.

### Run the home services with systemd

Install these user units under `~/.config/systemd/user/`. Replace all
placeholders, use absolute executable paths, and provision SSH host keys and
noninteractive authentication first. The SSH service uses the same complete
forward set as above; keep optional forwards consistent in both places.
These units are compatible with Debian 11 (systemd 247).

On one Debian 11 machine, `systemctl --user` failed with "Failed to connect
to bus: No such file or directory", although the user manager was running and
`XDG_RUNTIME_DIR` was set. The `dbus-user-session` package wasn't installed;
installing it wasn't tried, and that machine uses the system-unit form below.
To start user units at boot without logging in, enable lingering as in
[the primary's service](always-on.md#a-systemd-service).

You can instead install the same three files as system units in
`/etc/systemd/system/`. Add `User=<your-user>` to both services, replace
`%h` with the home directory's absolute path, use
`WantedBy=multi-user.target` in the target, and run the commands below with
`sudo` and without `--user`. For boot ordering, add `Wants=network-online.target` and
`After=network-online.target` to both services' `[Unit]` sections. This
doesn't guarantee the LAN address is ready, so keep the five-second restart.

```ini
# seestar-home.target
[Unit]
Description=Home Seestar forwarding and discovery
Wants=seestar-home-ssh.service seestar-home-discovery.service

[Install]
WantedBy=default.target
```

```ini
# seestar-home-ssh.service
[Unit]
Description=Seestar TCP forwards to the primary
PartOf=seestar-home.target

[Service]
Restart=always
RestartSec=5
ExecStart=/usr/bin/ssh -N -T -o BatchMode=yes -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -L <home-lan-ip>:4700:127.0.0.1:4700 \
  -L <home-lan-ip>:4800:127.0.0.1:4800 \
  -L <home-lan-ip>:4804:127.0.0.1:4804 \
  -L <home-lan-ip>:4801:127.0.0.1:4801 \
  -L <home-lan-ip>:4554:127.0.0.1:4554 \
  -L <home-lan-ip>:80:127.0.0.1:8080 <ssh-user>@<primary-host>
```

```ini
# seestar-home-discovery.service
[Unit]
Description=Seestar scan replies on the home LAN
PartOf=seestar-home.target

[Service]
Restart=always
RestartSec=5
ExecStart=%h/bin/seestar-wormhole discovery --bind <home-lan-ip> \
  --advertise-serial <serial> --advertise-model "<model-label>" \
  --advertise-name "<device-name>"
KillSignal=SIGINT
TimeoutStopSec=10
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now seestar-home.target
journalctl --user -u seestar-home-ssh -u seestar-home-discovery -f
```

The target starts both services; `PartOf` makes its stop/restart apply to
both. Each service restarts independently after five seconds, even after a
clean exit. SSH must stay in the foreground: if your `ssh_config` sets
`ForkAfterAuthentication` (OpenSSH 8.7 and later), add
`-o ForkAfterAuthentication=no`; older OpenSSH, such as Debian 11's 8.4,
rejects that option.
Discovery advertising while SSH reconnects is acceptable; a LAN row does
not prove readiness. Use `systemctl --user stop seestar-home.target` to stop
both deliberately. See [user-service logout behavior](always-on.md#a-systemd-service).
After an address or identity change, restart the target and select a fresh LAN row.

## What hasn't been tried

See [phone coverage](status.md#phone-app) and [platform coverage](status.md#platforms).

Details on how discovery answers, what it reports, and its rate limits are in
[the reference](reference.md#lan-discovery).
