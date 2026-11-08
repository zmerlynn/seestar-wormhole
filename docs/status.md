# Status

See [known issues and accepted limits](known-issues.md) for the limits that
remain by choice.

What's been tested and what hasn't, as of November 2026. The short version is
in the [README](../README.md#status).

## Telescopes

Daily-use testing was on one **Seestar S50 Pro, firmware 9.31**. The same
telescope now runs **firmware 9.75**, with the narrower coverage below. Other
Seestar models on the same cloud service are expected to work, since they
share the connection, but operation on other models has not been observed.

In November 2026, a user reported that a telescope shared with or rented to
their account was missing from the account telescope list in v0.5.0 and
appears in v0.6.0. They connected to it and used it through the proxy with
seestar_alp; see [seestar_alp and other clients](clients.md#seestar_alp).
v0.6.0 added both `isContainShared=3` to the list request and
`onlyQueryOwner=false` to the detail request; which one was needed hasn't been
tested separately. See [device selection](reference.md#device-selection).

### Firmware 9.75

The October 16, 2026 acceptance run checked:

- JSON-RPC control reads through the proxy: 40 of 40 replies correlated.
- Several concurrent control clients, with no cross-talk or drops.
- The previous site-pin behavior worked in that run.

The October 20, 2026 run, on a build of commit a3a85c9, checked:

- The DTLS handshake and session setup, from Linux and from macOS.
- JSON-RPC control reads, an SMB listing, and an rclone copy of 12 files
  (82 MB) with a download check: no differences.
- The iOS app over the LAN from a Mac: discovery found the telescope,
  the app connected and controlled it, and album images were served over
  port 80.
- Site protection on the device: the app's clock and location setters were
  dropped on every connection.

Always-on idle release/redial over days has not been tested on 9.75.
Camera-info reads worked on 9.31; they weren't part of the 9.75 runs.

## Phone app

- **iOS** works over the LAN from a Mac on firmware 9.31: discovery, control,
  live preview, the album, plans and video. On 9.75, discovery, control and
  the album have been checked.
- **Android** hasn't been run. Its protocol has been studied, but nobody has
  tried it with a real phone.
- **[Discovery at home with an SSH chain](phone-app.md#discovery-at-home-with-a-primary-on-another-lan)**
  works with a real phone on firmware 9.75: discovery, control, the album and
  a plan, with the proxy on another network and a Debian 11 home machine.
  The home services also run as system units with `User=` on Debian 11, and
  control through them has been checked.
  Live video and the user-unit form have not been checked.
- **Several phones at once** hasn't been tried. Through the proxy every phone
  shares a device-side address; [guest/master state also depends on the socket and client identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake/verification-and-roles.md#guest-and-master).
- **[Video over UDP](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/video.md#rtsp-endpoints)** isn't supported. The proxy forwards TCP only, and the app
  used TCP in testing.

## Platforms

Native macOS (arm64) and Windows CI jobs run build, vet and tests, then smoke
test the command's version and help output.

- **Linux** is where the proxy runs continuously, including overnight use.
  See [phone-app coverage](#phone-app) for the Debian 11 home SSH-chain test.
- **macOS** was tested natively by the maintainer on real hardware before the
  current Linux host. The phone-app setup ran on macOS 26 (arm64); LAN broadcast
  discovery answered an iPhone's scans, bound to the Mac's LAN address and to
  `0.0.0.0`. Discovery also has native loopback unicast and admission/response
  tests. The loopback broadcast socket fixture runs only on Linux.
- **Windows** has native CI coverage of DTLS interop and loopback TCP
  admission, including resets with full staging and byte preservation after FIN.
  See the [Windows requirements](reference.md#windows). Native Windows is CI-only:
  it has not been run against a real telescope or on a real LAN, so console
  handling and file permissions are untested in practice. Discovery's native
  loopback behavior is tested in CI:
  unicast reply address/port, malformed and oversized input, and cancellation.
  Real LAN behavior on Windows is not yet validated.
  Copy with the app, `seestar-wormhole-cli sync` or rclone; see
  [copying on Windows](copying.md#windows).
- **Containers** are checked for the image's user and startup. The phone setup
  hasn't been run in one.

## Copying

### The Copy, Browse and Free up space tabs and the file commands

The app's Copy, Browse and Free up space tabs, new in 0.7.0, haven't been run
against a telescope yet, on either route. Their copier came from seestar-copy
0.4.0, which before the merge was tried on a telescope only on macOS, through
the proxy. Its Free up space wasn't tried on a telescope, and neither was the
**On this network** route. In 0.7.0 its commands moved from
`seestar-copy-cli` into `seestar-wormhole-cli` (`sync`, `find`, `browse` and
`delete`) with the copy engine unchanged; the merged commands haven't been run
against a telescope either.

Two 0.7.0 additions are tested only in CI. The Mac app's Info.plist carries
the Local Network usage text, and a **Test**, **Save**, copy pass or Preview on
this network that nothing answered names the Local Network setting on a Mac;
neither has been seen on a Mac whose Local Network access is blocked. **Files
at once** (1 to 4, default 4) passes its value to the copier; no setting below
4 has been measured against a telescope, through Telescope Network or on this network.

CI covers both routes without a telescope:

- **On this network:** a Samba share set up like the telescope's (guest,
  read-only `EMMC Images`) stands in for it. Go tests copy from it, list it
  and delete from a writable scratch share, through the app's copier and through
  Free up space. Browser tests in Chromium and WebKit drive the app's page
  against it: saving and testing an address, copying with real passes,
  Preview, Clear history, Browse, Free up space's preview and confirmation,
  and a folder in use by `seestar-wormhole-cli sync` or a second app. They
  also cover `seestar-wormhole-cli browse`'s page.
- **Remotely through Telescope Network:** Go tests with a fake share and a stand-in
  connection check that copying dials Seestar port 445 through the run's
  session with the SMB port off, waits for the connection without starting
  it, and stays with its telescope, and that a
  delete holds the connection until it ends. No CI job copies through the
  proxy itself.
- **The window:** native scenarios on macOS, Windows and Linux run the real
  tabs against a fake share: they close the window, or quit on macOS, while a
  file is mid-copy, save a file from Browse, preview Free up space and cancel
  its confirmation, and show a folder in use by another app.
- **The file commands** of `seestar-wormhole-cli` are built and vetted for
  every release target, and `sync --help` is smoke tested natively on macOS
  and Windows.

### rclone and other SMB apps

See [Copy images](copying.md#with-rclone) for the recipe and its verification
status.
With public SMB clients through this proxy, the device selects SMB 3.1.1 when offered; it also accepts SMB 3.0.2 and 2.1 when each is the highest dialect offered. Observed October 16, 2026 with smbclient through this proxy, capped in turn at 3.1.1, 3.0.2 and 2.1; each was negotiated. This doesn't establish the device's configured minimum or maximum.
