# seestar-copy

seestar-copy copies the images from a ZWO Seestar's SMB share to a folder on
your computer. Its command-line tool, `seestar-copy-cli`, has one main command,
`sync`. It reads the telescope's share read-only, mirrors the files under a
folder you choose, and remembers what it has copied, so a file you move out of
that folder is not copied again. It repeats this every few minutes until you
stop it, so you can leave it running through a night of imaging.

It can reach the telescope on your local network, or through
[seestar-wormhole](https://github.com/zmerlynn/seestar-wormhole) when the
telescope is somewhere else.

## What it doesn't do

- It never writes to the telescope: it doesn't delete, rename or upload
  anything there. Freeing space on the telescope is up to you.
- It doesn't delete or move anything in your folder, sort images by night, or
  stack or process them. What you do with the files after they land is up to
  you.
- It isn't a general SMB client. It always logs in as guest to the
  telescope's `EMMC Images` share.
- It doesn't limit its bandwidth, and it has no configuration file: every
  setting is a flag.

## Install

Download the file for your platform from the
[Releases page](https://github.com/zmerlynn/seestar-copy/releases):

- **Linux:** `seestar-copy_<version>_linux_<arch>.tar.gz`, for amd64 or
  arm64. Unpack it.
- **Windows:** `seestar-copy_<version>_windows_<arch>.zip`, for amd64 or
  arm64. Unpack it. Windows downloads are not code-signed.
- **macOS:** `seestar-copy_<version>_darwin_universal.dmg`, for Intel and
  Apple silicon. It is signed and notarized by Apple. Open it.

Copy `seestar-copy-cli` (`seestar-copy-cli.exe` on Windows) to a folder on
your `PATH`, such as `/usr/local/bin` on Linux or macOS. Then check it with:

```sh
seestar-copy-cli --version
```

Every download also has the LICENSE and NOTICE files. To check a download's
checksum and build provenance, see
[verifying a release](docs/reference.md#verifying-a-release). The
[versioning policy](docs/versioning.md) says what a release promises.

### Build from source

You need [Go](https://go.dev/dl/) 1.27 or newer. Run:

```sh
git clone https://github.com/zmerlynn/seestar-copy
cd seestar-copy
go build -mod=vendor ./cmd/seestar-copy-cli
```

This creates `seestar-copy-cli` (`seestar-copy-cli.exe` on Windows) in that
folder; copy it to a folder on your `PATH` as above.

## Copy your images

1. Find the address to connect to. On your local network, that is the
   telescope's IP address and port 445, such as `192.0.2.10:445`. Through
   seestar-wormhole, it is the wormhole's SMB listener, `127.0.0.1:4445` by
   default.
2. Choose a folder for the images, such as `~/Seestar`. It is created if it
   doesn't exist.
3. Run one pass to check the connection:

   ```sh
   seestar-copy-cli sync --endpoint 127.0.0.1:4445 --dest ~/Seestar --once
   ```

4. Run it without `--once` to keep copying. Press Ctrl+C to stop it; files
   it was in the middle of are left for next time.

Files land under the folder as they are on the telescope, such as
`~/Seestar/MyWorks/M 31/`. Sync copies the whole share except a built-in list
of system, log and scratch files; add `--exclude` to leave out more, and see
[selection](docs/reference.md#selection) for the list. It waits until a file has stopped changing before it
copies it, so the first copy of a new image comes a few minutes after the
telescope writes it.

Each line of its log says what it copied, and each pass ends with a summary.
The [command reference](docs/reference.md) has every flag, log line and exit
code.

## Things to know

- **Moving files out is fine.** Sync remembers what it has copied in a state
  file kept outside your folder, not by looking at the folder. A file you move
  or delete is not copied again, unless it changes on the telescope. See
  [the state file](docs/reference.md#the-state-file).
- **An existing copy is reused.** If your folder already holds files copied
  another way with the same size and modification time, to within 2 seconds,
  sync records them without downloading them again. A different file at the
  same path is overwritten with the telescope's copy.
- **Use one spelling for your folder.** Give the same folder the same way
  every time, such as `--dest ~/Seestar`. Another spelling, such as through a
  link, starts a separate history; sync refuses to start when it notices. See
  [one spelling per destination](docs/reference.md#one-spelling-per-destination).
- **One sync per folder.** A second sync for the same folder, spelled the
  same way and with the same `--state-dir`, exits with an error. For a second
  telescope, use a second folder.
- **Partial files are hidden.** A file being copied is written under a hidden
  temporary name next to where it will go, so nothing that watches for `.fit`
  files sees half a file. See
  [temporary files](docs/reference.md#temporary-files).
- **Some names fail.** A name Windows can't store fails on Windows, and two
  names that differ only in upper and lower case fail everywhere. Sync never
  renames them; it logs them on every pass. See
  [names that fail](docs/reference.md#names-that-fail).
- **On Windows,** a power cut just after a copy can lose that file without
  sync noticing. See [Windows](docs/reference.md#windows).

## Status

Early development. Releases are 0.x, so a minor release may change a flag or
a file format; its notes say how. See [versioning](docs/versioning.md).

## Affiliation and warranty

seestar-copy isn't affiliated with, endorsed by or supported by ZWO. Seestar,
ZWO and other names are trademarks of their owners and are used only to
identify what this software works with.

It's provided as is, without warranty of any kind (see [LICENSE](LICENSE)).
Using it is at your own risk.

## More

- [Command reference](docs/reference.md): every flag, log line and exit code
- [Versioning](docs/versioning.md): what a release promises
- [Security policy](SECURITY.md) and [contributing](CONTRIBUTING.md)
- [How it works and how to hack on it](docs/development/README.md)
- [License](LICENSE) and [third-party notices](NOTICE);
  `seestar-copy-cli licenses` prints the notices of the libraries in your
  build
