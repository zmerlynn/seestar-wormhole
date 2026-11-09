# Copy images

seestar-wormhole copies the images from your Seestar's file share to a folder
on your computer in three ways:

- the app's [**Copy**, **Browse** and **Free up space**](#copy-in-the-app) tabs;
- [`seestar-wormhole-cli sync`](#from-the-command-line), the same copier for
  terminals, scripts and services, which comes in every download;
- [rclone or another SMB app](#with-rclone), through the proxy's SMB listener.

The app and `seestar-wormhole-cli` reach the telescope either remotely through
Telescope Network, sharing the proxy's connection, or directly on the network
the telescope is on. See [tested coverage](status.md#copying) for what has been
tried on each route.

## Copy in the app

1. **Choose how to reach the telescope.** On the **Connection** tab, under
   **Reach the telescope**, choose **Remotely through Telescope Network** and
   **Connect**, or choose **On this network**, enter the
   [telescope's address](#find-the-telescopes-address), and **Save**. Until
   one is saved, Copy, Browse and Free up space are unavailable and say so.
   Through Telescope Network, copying goes through the connection itself, so
   it doesn't need the SMB port, and waits for you to connect; it never
   connects for you. With **Connect when an app needs Seestar**, a check
   starts the session when it needs the telescope.
2. **Choose a folder.** The first time you open **Copy**, choose the folder for
   your images and **Save**. Nothing is copied yet: copying
   starts when you press **Start copying**. Each telescope has its own
   folder: a second one is asked for its folder the first time Copy reaches
   it. See [more than one telescope](#more-than-one-telescope).
3. **Start copying.** **Start copying** checks the telescope every 5 minutes
   by default and copies what is new; **Check now** checks at once, and **Stop
   copying** stops. To copy once instead, turn off **Check for new files
   every** in the settings: **Start copying** then copies what is new and
   stops. **Preview** shows what Start would copy without copying anything.
   Keep the app open while it copies.

Images land in the folder as they are on the telescope, such as
`~/Seestar/MyWorks/M 31/`. The telescope's logs, scratch and system files are
skipped by default. **Change…** opens the settings: the folder, how often to
check or whether to copy once, how many files to copy at once, what to leave out or only copy, and
whether to start copying when the app opens. [Using the UI](ui.md#copy) has every setting and message.

**Browse** shows the telescope's files one folder at a time, with its `.jpg`
images, `.txt` logs and FITS headers, and, once Copy's folder is saved, marks
which are copied, changed, new or left out. It changes nothing on the telescope; **Save** saves a file you
pick. See [Browse](ui.md#browse).

**Free up space** deletes images from the telescope once they are safely
copied. Copy first, make your own backup or export of the folder, then
**Preview what to delete**. It lists, per folder, the files whose copy in your
folder still matches and that haven't changed on the telescope, 7 days old or
more by default. **Keep on the telescope** and **Only free up** choose what
it may delete, with the same patterns as Copy's; they are separate from Copy's
**Leave out** and **Only copy**. Uncheck a folder to keep it. Nothing is deleted until you
choose **Delete N files…** and confirm. Avoid it while the telescope is busy.
See [Free up space](ui.md#free-up-space) and the
[proof modes](reference/files-cli.md#proof-modes).

Settings saved by seestar-copy 0.4.0's own app aren't imported. Choose the
folder and settings again in Copy.

## More than one telescope

Each telescope copies into its own folder, with its own copy history, so two
telescopes never mark each other's files as copied or overwrite them. The
app knows a telescope by its serial number, the same through Telescope
Network and on this network, so switching how you reach it keeps its folder.
An address saved without a serial is a telescope of its own.

- **The first telescope** keeps the folder you chose. After an upgrade, the
  telescope saved on the Connection tab keeps the folder and history it had,
  or, with none saved, the first telescope Copy reaches does. Nothing moves
  and nothing is copied again.
- **Another telescope** has no folder yet: Copy says so and asks for one
  before it copies, Free up space waits for it, and Browse lists the
  telescope's files without statuses. The folder field suggests one beside
  the first telescope's, named for the telescope, such as
  `Seestar S50 <serial>`, or `Seestar <host> <port>` for an address. Save
  refuses another telescope's folder, or a folder inside or around one.
- **An address that turns out to be a telescope you know.** When a scan or
  Test hears a serial number at an address that has a folder of its own,
  and that telescope has none, Copy offers the address's folder. Nothing
  changes until you choose **Use the address's folder**, since the router
  may have given the address to another telescope.
- **Switching telescopes** stops copying, as any change of telescope does;
  **Start copying** then uses the new telescope's folder, or asks for one.
- **Shared by all telescopes:** the copy history folder, how often to check,
  files at once, and the patterns to leave out or only copy.

After a downgrade, an older app reads only the first telescope's folder, so
every telescope copies into it again.

## Things to know

These hold for the app and for `seestar-wormhole-cli sync`, which use the same copy
history unless the app's **Copy history folder** or sync's `--history-dir`
names another.

- **Moving files out is fine.** The copy history is kept outside your folder,
  so a file you move or delete is not copied again, unless it changes on the
  telescope or you choose another copy history folder, which starts a fresh
  history. See [copy history](reference/selection-and-history.md#copy-history).
- **An existing copy is reused.** A file already in your folder with the same
  size and modification time, to within 2 seconds, is recorded without
  downloading it again. A different file at the same path is overwritten with
  the telescope's copy.
- **One copy per folder.** While the app or a `sync` copies to a folder, the
  other can't, and says the folder is in use. Free up space and
  `delete --force` run between checks. A lock file, `.seestar-wormhole.lock`,
  stays in the folder; leave it there. Stop any `seestar-copy-cli`, or an
  older seestar-wormhole, first: they don't take that lock. See
  [locks](reference/selection-and-history.md#locks).
- **Partial files are hidden.** A file being copied is written under a hidden
  temporary name next to where it will go, so nothing that watches for `.fit`
  files sees half a file. A file partly copied when copying stops is copied
  again next time.
- **Some names fail.** A name Windows can't store fails on Windows, and two
  names that differ only in upper and lower case fail everywhere. They are
  never renamed. See [names that fail](reference/files-cli.md#names-that-fail).
- **On a Mac, allow Local Network access.** The app asks macOS for Local
  Network access the first time it reaches an address on this network; allow
  it. If it is blocked, **Test**, Copy, Preview, Browse and Free up space
  can't reach the telescope there, and on a Mac they say where to allow it:
  enable seestar-wormhole in System Settings → Privacy & Security → Local
  Network, then **Test** again. A blocked scan says so too. While the
  request is open, the first connection after the app starts, from any tab,
  waits up to 5 seconds for your answer. When macOS refuses
  `seestar-wormhole-cli scan`'s request, it says so and exits 1. Through Telescope
  Network, copying doesn't need it.
- **Through Telescope Network, a large copy is shared.** It uses the same telescope
  session as the phone app and control clients, and delays their status
  replies. **Files at once** in Copy settings sets how many files copying
  runs at once, 4 by default: if the Seestar app or seestar_alp is slow to
  respond while copying, lower it to 1 or 2.

## Find the telescope's address

On this network, the app and `seestar-wormhole-cli` need the telescope's address.
Through Telescope Network they use the proxy's listener instead.

When the telescope is on your home Wi-Fi, your router gave it its address.
The app's **On this network** scans for it and lists each telescope that
answers, as the phone app finds them; pick its address. From the command
line, `./seestar-wormhole-cli scan` lists them, one address per line:

```text
192.0.2.10 - S50_Garden "Seestar S50"
```

A scan reaches only this computer's own networks, and some routers and
guest networks don't pass it through. If nothing answers, open the router's
list of connected devices and find the telescope's entry, not this
computer's, with an address such as `192.0.2.10`. In the app, **Test**
checks that the address answers file sharing. On a Mac, scanning needs
Local Network access; see [things to know](#things-to-know).

An address picked from a scan is saved with the telescope's serial number.
From then on, the app scans for that serial and uses the address the
telescope answers from, the saved one first, so a new address from the
router is followed and saved; `--profile default` on the command line
follows it the same way for that run. If another telescope answers at the
saved address, it isn't used. If the scan doesn't see the telescope, the
saved address is tried. Free up space deletes only where its preview
connected; if the telescope has moved since, preview again.

By default it scans once when copying starts, and once when Browse opens or
Free up space checks, and again only when a connection fails. **Scan for
the saved telescope** under **On this network** can make it scan before
every connection, or never: then only **Scan** and **Scan again** send
scan requests. File operations still connect to the saved address, and a
telescope that moves isn't followed. On the command line, `--scan=start`,
`--scan=connect` or `--scan=off` chooses for one run.

The app takes the address alone and uses SMB port 445 unless you add another
port. `seestar-wormhole-cli` needs the port too: `--endpoint 192.0.2.10:445`.

## From the command line

`seestar-wormhole-cli` comes in every download. Run these commands from the
folder that holds it; on macOS, copy it off the disk image first, as in
[Install](../README.md#install). In PowerShell, write
`.\seestar-wormhole-cli.exe` for `./seestar-wormhole-cli`. Earlier releases
had these commands in a separate `seestar-copy-cli`.

The file commands (`sync`, `find`, `browse` and `delete`) don't sign in or
connect through Telescope Network themselves. Through Telescope Network, keep the app connected, or run
`./seestar-wormhole-cli proxy` from the tools' folder in another terminal
(see [Sign in](../README.md#sign-in)) while the copy command runs. In
PowerShell, use `.\seestar-wormhole-cli.exe proxy`. For a telescope on this
network, give its address directly; no proxy or account is needed.

1. Find the address to connect to. On the telescope's network, that is
   [its address](#find-the-telescopes-address) and port 445, such as
   `192.0.2.10:445`. Through seestar-wormhole, it is the proxy's SMB
   listener, `127.0.0.1:4445` by default.
2. Run one pass to check the connection:

   ```sh
   ./seestar-wormhole-cli sync --endpoint 127.0.0.1:4445 --dest ~/Seestar --once
   ```

   In PowerShell:

   ```powershell
   .\seestar-wormhole-cli.exe sync --endpoint 127.0.0.1:4445 --dest "$HOME\Seestar" --once
   ```

3. Run it without `--once` to keep copying. Press Ctrl+C to stop it.

To keep copying without a terminal open, run `sync` as a service; see
[keep copying without the app](always-on.md#keep-copying-without-the-app).

To look through the telescope's files in your web browser:

```sh
./seestar-wormhole-cli browse --endpoint 127.0.0.1:4445 --open
```

To free space on the telescope, run sync, make your own backup of the folder,
then preview with
`./seestar-wormhole-cli delete --endpoint 127.0.0.1:4445 --dest ~/Seestar`, which
only reports what it would delete. When the preview looks right, add
`--force`.

The [file command reference](reference/files-cli.md) has every command, flag,
log line and exit code; `find` lists the share for scripts.

## With rclone

Recipe verified 2026-10-18 through the v0.1.0 proxy; the device did
not report its firmware version during the run (last observed: 9.75).

[rclone](https://rclone.org/) connects to the proxy's SMB listener and shares
the proxy's existing telescope session. The [proxy setup](reference/connection-cli.md#proxy)
provides the listener; the default mapping is local port 4445 to device port
445.

### Configure the SMB connection

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

### Copy with rclone

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

To use another file-sharing app instead of the Copy tab, keep
seestar-wormhole connected and use the settings below. We use rclone (above);
any SMB app should work with these settings.

| Setting | Value |
| --- | --- |
| Protocol/profile | SMB (No Encryption) |
| Server | `127.0.0.1` — Use this when copying on the same computer as seestar-wormhole. |
| Port | The port shown under **Copying files** (`4445` by default) |
| Username | `guest` |
| Password | Empty |
| Path | `/EMMC Images/MyWorks` |

If **Listen on** is set to **Other…**, use that address for Server, even when
copying on the same computer.

To copy images to another computer, use the network address of the one running
seestar-wormhole. In seestar-wormhole, set **Listen on** to **All networks** or
that address.

- **macOS and Windows: [Cyberduck](https://cyberduck.io/)** (free). In
  Preferences → Profiles, enable **SMB (No Encryption)**. Open Connection,
  choose that profile, and enter the connection settings above, including
  Path under More Options. Connect, select an image, and choose Download to
  a local folder.
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

On Windows, copy with the app's Copy tab, `seestar-wormhole-cli sync` or rclone. The
built-in SMB client reaches an
alternate TCP port only on Windows 11 24H2 or later and Windows Server 2025.
Its alternate-port mapping requires an administrator, for example:

```bat
NET USE Z: "\\<bind>\EMMC Images" "" /USER:guest /TCPPORT:4445
```

Windows blocks guest logons and unsigned sessions by default. This route also
requires machine-wide security policy changes, which this page does not
recommend.
