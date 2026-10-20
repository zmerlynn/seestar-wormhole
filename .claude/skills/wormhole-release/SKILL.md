---
name: wormhole-release
description: Cut a seestar-wormhole release - gates, a reproducible build, release notes with a compatibility record, one owner check before the tag, then the tag, a checked draft and publication.
---

# Releasing seestar-wormhole

A checklist the release preparer runs from start to finish. There is one
human check, before the tag is pushed. After that the release runs through
to publication, and any failed check stops it. [Cutting a release](../../../docs/development/releasing.md)
has the mechanics; this file is the order and the checks.

## 1. Pick the commit and the version

- Use a commit on `origin/main` whose CI is green, and write down its full SHA.
- List the changes since the last tag: `git log --oneline vX.Y.Z..<sha>`.
- Choose the version by semantic versioning. A changed or removed command,
  flag, exit code, reason or log record is breaking.

## 2. Run the gates

Run these in a clean worktree at that SHA, in `golang:1.27.2` with
`--network none`. Follow steps 1-4 of [releasing.md](../../../docs/development/releasing.md):

- [ ] build, vet and test pass, along with the release test and `./scripts/test-check.sh`;
- [ ] `./scripts/release.sh vX.Y.Z` produces six archives, and `sha256sum --check` passes;
- [ ] a second run produces byte-identical archives (`cmp` each file);
- [ ] `--version` prints the version, commit and platform, and `licenses` lists `github.com/pion/dtls/v3`.

## 3. Check the compatibility record

- [ ] `docs/status.md` on that commit records a run with a real telescope on
  the current firmware. If it doesn't, run a smoke test and land the status
  update first; the release commit moves to that merge.
- [ ] Keep track of what was tested with this build, what is carried forward
  from older runs, and what was never tested.

## 4. Draft the notes

Write them outside the checkout, for users rather than developers:

1. One summary line.
2. **Changes:** one or two plain sentences each, linking the reference
   section that has the detail. Leave out internals such as forks, suite
   names and log record names.
3. **Breaking changes**, or "No breaking changes."
4. **Install:** download, unpack, `--version`, the account variables, and
   the no-authentication warning. Point to
   [verifying a release](../../../SECURITY.md#verifying-a-release) without
   repeating its commands.
5. **Compatibility record:** the date, model and firmware, a short tested
   list, and what was not tested, linking `docs/status.md`.
6. The no-affiliation line.

Link docs at the tag (`blob/vX.Y.Z/...`), not at `main`.

## 5. Owner check

Show the owner the SHA, the version, the gate results and the notes. Apply
their edits. Nothing after this step needs another approval; a failed check
stops the release and goes back to the owner.

## 6. Tag

```sh
git tag -a vX.Y.Z -m "seestar-wormhole vX.Y.Z" <sha>
git push origin vX.Y.Z
```

Never move or delete a pushed tag. A broken release is fixed with a new patch version.

## 7. Check the draft and publish

When the release workflow finishes, check:

- [ ] the run succeeded, and the draft has six archives plus `SHA256SUMS`;
- [ ] the downloaded archives pass `sha256sum --check`, and the published
  `SHA256SUMS` is byte-identical to the local one;
- [ ] `gh attestation verify <archive> --repo zmerlynn/seestar-wormhole`
  passes for every archive.

Then apply the notes and publish:

```sh
gh release edit vX.Y.Z --notes-file NOTES.md
gh release edit vX.Y.Z --draft=false --latest
```

Confirm that `releases/latest` is the new tag, send the owner the link, and
delete the worktree and `dist/`.
