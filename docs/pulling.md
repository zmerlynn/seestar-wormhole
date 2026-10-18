# Pulling images

The Seestar keeps its images under `MyWorks` on its internal storage, [shared
over SMB](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#shares) (ordinary network file sharing). `seestar-wormhole pull` copies them
to your computer. It's built for doing that over and over, every morning or
every few minutes through the night, without re-downloading what you already
have and without ever deleting anything on the telescope.

`pull` includes an SMB client and connects directly unless `--via` points to an existing proxy; see [pull](reference.md#pull).

## The everyday case

With the proxy already running (see the [always-on guide](always-on.md)):

```sh
./seestar-wormhole pull --via 127.0.0.1:4445 --since 24h --verify ./MyWorks
```

This command reads the last 24 hours and verifies copied bytes; see [pull option meanings](reference.md#pull-flags).

Each image lands at `./MyWorks/<target folder>/<file>`, mirroring the
telescope's layout.

Without a proxy running, leave out `--via` and `pull` connects by itself.
Export your credentials first:

```sh
./seestar-wormhole pull --busy-wait 90s --timeout 30m --verify ./MyWorks
```

`--busy-wait` bounds acquisition waits and `--timeout` bounds the work; see [direct acquisition budgets](reference.md#busy-timeout-and-cancellation).

## Images and dark sequences

Use `--dir` for dark sequences in `Autorun/Dark` and the telescope's master
darks in `darklibrary`:

```sh
./seestar-wormhole pull --via 127.0.0.1:4445 \
  --dir MyWorks --dir Autorun/Dark --dir darklibrary --verify ./archive
```

Each `--dir` selects a share-relative directory and keeps that path beneath the destination; see [explicit directory layout](reference.md#pull).

The [temperature dark-library section](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/calibration-frames/dark-library.md#temperature-dark-library-firmware-975)
describes the master files this command copies; `Autorun/Dark` holds separate
individual frames. `darklibrary` was first observed after the telescope made its first master, so a pull before then
can fail with `missing_directory` (exit 1).

Without `--dir`, MyWorks contents go directly into the destination; with `--dir`, each directory is preserved beneath it. See [layouts](reference.md#pull).

A destination stays bound to its original source and layout; use a new destination to switch layouts. See [bindings](reference.md#destination-state-and-safe-paths).

`--only` filters immediate child folders across every selected directory; see [filter semantics](reference.md#selection-and-time-windows).

## What it's careful about

Receipts let later runs skip unchanged files; use `--verify` to reread and compare bytes. See [receipts and verification](reference.md#copies-receipts-and-verification).

## Just some targets

`--only` takes a regular expression matched against the target folder names:

```sh
./seestar-wormhole pull --via 127.0.0.1:4445 --only '^M 31' ./MyWorks
./seestar-wormhole pull --via 127.0.0.1:4445 --only 'Dark' --only 'Flat' ./calibration
```

Several `--only` expressions form a case-sensitive union; anchor a pattern such as `^M 31$` to match one folder exactly. See [regexp semantics](reference.md#selection-and-time-windows).

Use `--dry-run --log-level debug` to plan without local writes; per-file plans go to stderr. See [dry-run output](reference.md#summary-and-progress).

## Pulling through the night

One pattern is to run one proxy all night, with a loop that pulls every
few minutes while the telescope works. Fix the start of the night with a real
timestamp rather than `24h`, so the window doesn't slide as the night goes on:

```bash
#!/usr/bin/env bash
set -u
dest=/data/tonight
since=2026-10-14T00:00:00Z     # the start of the night, in UTC

rc=0
for pass in $(seq 200); do
  ./seestar-wormhole pull --via 127.0.0.1:4445 --since "$since" \
    --timeout 45m --output json --log-level warn "$dest" \
    > latest.json 2> latest.log
  rc=$?
  case $rc in
    0|1|3) sleep 300 ;;   # done, failed, or busy: try again in five minutes
    *)     exit $rc ;;    # bad options, or you pressed Ctrl+C
  esac
done
exit $rc
```

`--output json` writes a machine-readable summary; check its status and reason after each pass. See [summary and failure output](reference.md#summary-and-progress).

A clean pass doesn't mean the night is over: the telescope may still be
writing. Stop the loop after the telescope has finished and a pass or two
have come back with nothing deferred.

### --since auto

`--since auto` resumes from saved checkpoints but scans fully on the first run; see [auto checkpoint selection](reference.md#selection-and-time-windows).

## Downloading while you observe

[Everything shares the telescope's one connection](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause), so a big download can slow
down status replies to the app or seestar_alp. One evening's measurement was
taken while downloading with standalone rclone through the proxy rather than
`pull` itself, so the numbers are rough. Typical status replies took about 0.2
to 0.4 seconds on a direct connection while the download ran flat out, and
0.5 to 1 second when the cloud relayed the connection. None of the measured
replies missed a three-second deadline.

If responsiveness matters more than speed:

- Use `--transfers 1`. The default is 4; in testing, four ran about twice as
  fast as one.
- `--bwlimit` can help on a direct connection, for example `--bwlimit 1MiB/s`.
  Halving the rate brought status replies down to about 0.15 to 0.2 seconds,
  at half the download speed. It didn't help over the relay.
- Or just pull when you're not actively driving the telescope.

For a sense of speed, one transfer ran at about 2 MB/s and four reached about
4 MB/s. More than four didn't help.

## Other tools

If you prefer rclone or another SMB client, connect to the proxy SMB port with guest access and an empty password; see [standalone SMB client details](reference.md#standalone-smb-clients).

```sh
rclone --config seestar.conf config create seestar smb host 127.0.0.1 port 4445 user guest
rclone --config seestar.conf copy "seestar:EMMC Images/MyWorks" ./MyWorks
```

Windows Explorer can't connect to a port other than 445. Neither Explorer nor
Finder can limit its download speed.

Every flag, exit code and summary field, plus the fine print on time windows,
checkpoints and rebinding, is in [the reference](reference.md#pull).
