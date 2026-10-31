# seestar_alp and other clients

To a client, the proxy looks like the telescope. Each of the telescope's
network ports gets a matching port on your computer. A client that knows how
to talk to a Seestar on the local network can talk to it through the proxy,
as long as you point it at the right address and port.

## seestar_alp

[seestar_alp](https://github.com/smart-underworld/seestar_alp) connects to the
telescope's control port (4700), imaging port (4800) and log port (4801). The
proxy forwards all three by default, so it needs no `-p` flags:

```sh
./seestar-wormhole-cli proxy
```

Then, in seestar_alp's configuration, set the telescope's address to
`127.0.0.1`. Field names vary between seestar_alp versions, so check its own
instructions for where that goes.

seestar_alp's live view asks for RTSP over UDP by default (`rtsp_udp = true`
under `[network]`). The proxy forwards TCP only, so set `rtsp_udp = false`.
Video through the proxy has not been tested.

The proxy forwards ordinary client traffic and inspects selected control requests for site protection; see [the protocol boundary](reference.md#proxy).

Not every seestar_alp feature has been tested this way. If something
doesn't work through the proxy that works on the local network, please file
an issue.

## NINA and other Alpaca clients

Point them at seestar_alp's own Alpaca server. It runs on your computer and
reaches the telescope through the proxy.

The telescope's built-in Alpaca server, on port 32323 when it is enabled in
the telescope's settings, isn't forwarded by default; `-p 32323:32323` adds
it. Alpaca discovery doesn't pass through the proxy, so enter the address and
port by hand. Neither route has been tested.

## Your own scripts

[The control port speaks the telescope's line-based JSON-RPC](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake.md), exactly as it
does on the local network. The example uses the [default proxy listener](reference.md#proxy-flags).

See [pi_get_time](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#reading-the-time-pi_get_time) for the wire rules.

```sh
printf '%s\r\n' '{"id":1,"method":"pi_get_time"}' | nc -w 2 127.0.0.1 4700
```

The proxy removes site and time changes from the inspected JSON requests; see [the inspected request rules](reference.md#site-protection).

## Ports

Map each client port with `-p local:device`; the default mappings include the control and imaging ports. See [port mappings](reference.md#port-mappings).

## Things to know

The proxy holds one shared session, so stop it before returning to the app’s remote mode; downloads can slow status replies. See [handover](reference.md#availability-and-busy-devices) and [download responsiveness](copying.md). Video transport coverage is listed [here](status.md#phone-app). Keep it on a [trusted network](reference.md#proxy).
