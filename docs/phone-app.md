# Using the phone app from home

The trick here is that the phone app has [two ways to find a telescope](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#address-entry-saved-devices-and-lan-versus-remote): over
the cloud (remote mode) and on the local Wi-Fi (the LAN list). seestar-wormhole
holds the cloud connection itself and then shows up in the LAN list, so the app
treats a telescope thousands of miles away as if it were in the next room.

Phone use requires LAN discovery; the tested phone platforms and paths are listed in [phone coverage](status.md#phone-app).

## What you need

- A computer on the same Wi-Fi network as your phone. The tested computer was
  a Mac. Linux or a Raspberry Pi should work the same way, though neither has
  been tested. Windows
  doesn't support LAN discovery.
- Your Seestar account email and password.
- A correct site stored on the telescope. The device keeps that site and its
  own clock. To change the site, connect the app to the device without the
  proxy; the [README](../README.md#before-you-start-your-account-and-your-site)
  explains why.

## Start the proxy

Enter the account credentials as described in the [README](../README.md#before-you-start-your-account-and-your-site), then start the proxy with discovery enabled:

Then start the proxy with discovery turned on:

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

## What hasn't been tried

Windows and container phone discovery have not been tested; see [untested phone paths](status.md#phone-app).

Details on how discovery answers, what it reports, and its rate limits are in
[the reference](reference.md#lan-discovery).
