# Copy images with rclone

Recipe verified on firmware: pending

[rclone](https://rclone.org/) is the supported copying route on Linux, macOS,
and Windows. It connects to the proxy's SMB listener and shares the proxy's
existing telescope session. The [proxy setup](reference.md#proxy) provides the
listener; the default mapping is local port 4445 to device port 445.

## Configure the SMB connection

The remote uses `smb host <bind> port 4445 user guest`, with an empty password.
`<bind>` is the proxy address reachable from the copying computer. With the
default loopback bind, configuration and listing look like this:

```sh
rclone --config seestar-rclone.conf config create seestar smb host 127.0.0.2 port 4445 user guest
rclone --config seestar-rclone.conf lsf "seestar:EMMC Images/MyWorks" --recursive --files-only
```

The share is `EMMC Images`; `MyWorks` is the default image directory used in
this recipe. Another share-relative directory can replace `MyWorks` in the
source path. The rclone remote is a local client configuration, separate from
the proxy's account credentials and installation identity.

## Copy and confirm the bytes

This example copies files modified within the last day. `--min-age 2m` skips
recent files that may still be written by the telescope.

```sh
rclone --config seestar-rclone.conf copy "seestar:EMMC Images/MyWorks" ./MyWorks \
  --max-age 24h --min-age 2m --transfers 4 --multi-thread-streams 0 --low-level-retries 3
```

`--max-age` selects the lookback window. `--transfers 4` limits concurrent file
copies; `--multi-thread-streams 0` disables splitting a file across streams;
`--low-level-retries 3` bounds low-level retries. Optional `--bwlimit 2M` limits
bandwidth, and `-P` displays progress. Repeating `copy` copies new or changed
files without deleting source or destination files.

A large copy shares the telescope session with control clients and delays
status replies. A smaller time window or a bandwidth limit reduces contention;
large transfers can also run while control clients are idle.

A download check rereads the selected source and destination files to confirm
bytes, rather than only comparing sizes:

```sh
rclone --config seestar-rclone.conf check --download "seestar:EMMC Images/MyWorks" ./MyWorks \
  --max-age 24h --min-age 2m --transfers 4 --multi-thread-streams 0 --low-level-retries 3
```

A SHA-256 manifest of the local copy can be retained separately:

```sh
rclone hashsum sha256 ./MyWorks > MyWorks.sha256
```

## Interactive SMB use

`smbclient` also accepts the alternate port and empty guest password:

```sh
smbclient '//127.0.0.2/EMMC Images' -p 4445 -U guest%
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
