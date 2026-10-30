# Copy images

Recipe verified 2026-10-18 through the v0.1.0 proxy; the device did
not report its firmware version during the run (last observed: 9.75).

[rclone](https://rclone.org/) is the supported copying route on Linux, macOS,
and Windows. It connects to the proxy's SMB listener and shares the proxy's
existing telescope session. The [proxy setup](reference.md#proxy) provides the
listener; the default mapping is local port 4445 to device port 445.

## Configure the SMB connection

The remote uses `smb host <bind> port 4445 user guest`, with an empty password.
`<bind>` is the proxy address reachable from the copying computer. With the
default loopback bind, configuration and listing look like this:

```sh
rclone --config seestar-rclone.conf config create seestar smb host 127.0.0.1 port 4445 user guest
rclone --config seestar-rclone.conf lsf "seestar:EMMC Images/MyWorks" --recursive --files-only
```

The share is `EMMC Images`; `MyWorks` is the default image directory used in
this recipe. Another share-relative directory can replace `MyWorks` in the
source path. The rclone remote is a local client configuration, separate from
the proxy's account credentials and installation identity.

## Copy

This example copies files modified within the last day. `--min-age 2m` skips
recent files that may still be written by the telescope.

```sh
rclone --config seestar-rclone.conf copy "seestar:EMMC Images/MyWorks" ./MyWorks --max-age 24h --min-age 2m --transfers 4 --multi-thread-streams 0 --low-level-retries 3
```

`--max-age` selects the lookback window. `--transfers 4` limits concurrent file
copies; `--multi-thread-streams 0` disables splitting a file across streams;
`--low-level-retries 3` bounds low-level retries. Optional `--bwlimit 2M` limits
bandwidth, and `-P` displays progress. Repeating `copy` copies new or changed
files without deleting source or destination files.

A large copy shares the telescope session with control clients and delays
status replies. A smaller time window or a bandwidth limit reduces contention;
large transfers can also run while control clients are idle.

rclone compares each file's size after it transfers. The proxy's connection to
the telescope authenticates every packet, so routine copies do not need a
second read. A download check rereads the selected source and destination files
and compares their bytes, for example after upgrading the proxy. It reads every
file from the telescope again, roughly doubling the time:

```sh
rclone --config seestar-rclone.conf check --download "seestar:EMMC Images/MyWorks" ./MyWorks --max-age 24h --min-age 2m --transfers 4 --multi-thread-streams 0 --low-level-retries 3
```

A SHA-256 manifest of the local copy can be retained separately:

```sh
rclone hashsum sha256 ./MyWorks --output-file MyWorks.sha256
```

The manifest is written outside the copied directory, so it does not list
itself. rclone checks the manifest against that directory:

```sh
rclone hashsum sha256 ./MyWorks --checkfile MyWorks.sha256
```

## Copy with a graphical app

To copy on the computer running seestar-wormhole, keep it connected and use
the connection settings below. We use rclone (above); any SMB app should work
with these settings.

| Setting | Value |
| --- | --- |
| Protocol/profile | SMB (No Encryption) |
| Server | `127.0.0.1` — This computer (the computer running seestar-wormhole) |
| Port | The port shown under **Copying files** (`4445` by default) |
| Username | `guest` |
| Password | Empty |
| Path | `/EMMC Images/MyWorks` |

To copy from another computer, use the address of the computer running
seestar-wormhole; it must Listen on **All networks** or that address.

- **macOS: [Cyberduck](https://cyberduck.io/)** (free). In Preferences →
  Profiles, enable **SMB (No Encryption)**. Open Connection, choose that
  profile, and enter the connection settings above, including Path under
  More Options. Connect, select an image, and choose Download to a local folder.
- **Windows: [Mountain Duck 5](https://mountainduck.io/)** (paid, with a
  trial). Its per-user installer needs no administrator rights. Install
  Mountain Duck 5 and choose **Integrated** mode. Create an **SMB (No
  Encryption)** bookmark using the connection settings above. Open it in
  File Explorer, then copy an image to a local folder.
- **Linux: Files (Nautilus) or Dolphin.** The file manager's SMB support
  (GVfs or KIO) must be installed. Press Ctrl+L and enter
  `smb://<Server>:<Port>/EMMC%20Images/MyWorks/`, replacing Server and Port
  with the values from the connection settings above. Sign in with that
  Username and empty Password, then copy an image to a local folder.
- **Any system: rclone's built-in GUI.** With the configuration from
  [Configure the SMB connection](#configure-the-smb-connection), run
  `rclone --config seestar-rclone.conf gui`, browse the `seestar` remote, and
  download an image from `EMMC Images/MyWorks` to a local folder.

Open the downloaded image from your local folder to check the copy.

As with rclone, a large copy delays status replies to control clients.

## Interactive SMB use

`smbclient` also accepts the alternate port and empty guest password:

```sh
smbclient '//127.0.0.1/EMMC Images' -p 4445 -U guest%
```

## Windows

rclone is the supported route on Windows. The built-in SMB client reaches an
alternate TCP port only on Windows 11 24H2 or later and Windows Server 2025.
Its alternate-port mapping requires an administrator, for example:

```bat
NET USE Z: "\\<bind>\EMMC Images" "" /USER:guest /TCPPORT:4445
```

Windows blocks guest logons and unsigned sessions by default. This route also
requires machine-wide security policy changes, which this page does not
recommend.
