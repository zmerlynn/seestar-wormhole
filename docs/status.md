# Status

What's been tested and what hasn't, as of October 2026. The short version is
in the [README](../README.md#status).

## Telescopes

Daily-use testing was on one **Seestar S50 Pro, firmware 9.31**. The same
telescope now runs **firmware 9.75**, with the narrower coverage below. Other
Seestar models on the same cloud service are expected to work, since they
share the connection, but operation on other models has not been observed.

### Firmware 9.75

The October 16, 2026 acceptance run checked:

- JSON-RPC control reads through the proxy: 40 of 40 replies correlated.
- Several concurrent control clients, with no cross-talk or drops.
- A full pull with verification: 108 files, with hashes matching the same
  files from a firmware 9.31 run.
- Ctrl-C cancellation of a pull: exit 130, status `canceled`.
- The previous site-pin behavior worked in that run. Current site protection
  drops app setters and is covered by offline tests; it has no device-run
  coverage yet.

On the night of October 16/17, the proxy ran all night while MyWorks and
`--dir Autorun/Dark` were repeatedly pulled through `--via`. Files copied and
verified.

Pulling `--dir darklibrary` through `--via` was also verified on firmware 9.75.

The phone app through the proxy, LAN discovery from the phone, and always-on
idle release/redial over days have not been tested on 9.75. Camera-info reads
worked on 9.31; they weren't part of the 9.75 run.

## Phone app

- **iOS** works over the LAN from a Mac on firmware 9.31: discovery, control,
  live preview, the album, plans and video.
- **Android** hasn't been run. Its protocol has been studied, but nobody has
  tried it with a real phone.
- **Several phones at once** hasn't been tried. Through the proxy every phone
  shares a device-side address; [guest/master state also depends on the socket and client identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake/verification-and-roles.md#guest-and-master).
- **[Video over UDP](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/video.md#rtsp-endpoints)** isn't supported. The proxy forwards TCP only, and the app
  used TCP in testing.

## Platforms

- **Linux** is where the proxy runs continuously, including overnight pulls.
  The phone setup hasn't been run from Linux, though it should work the
  same way.
- **macOS** has run the phone-app setup. `pull` hasn't been run there against
  the telescope.
- **Windows** cross-builds, and a selection of tests pass under Wine. It
  hasn't been run natively or against a real telescope, so its console
  handling and file permissions are untested in practice. It has no LAN
  discovery, and Explorer can't reach SMB on a port other than 445, so use
  `pull` for images.
- **Containers** are checked for the image's user and startup. The phone setup
  hasn't been run in one.

## pull

See [pull behavior limits](reference.md#pull).
With public SMB clients through this proxy, the device selects SMB 3.1.1 when offered; it also accepts SMB 3.0.2 and 2.1 when each is the highest dialect offered. These observations establish the tested proxy interoperability.
