---
name: seestar-copy-release
description: Cut a seestar-copy release - gates, reproducible CLI builds and the Mac DMG, release notes with a compatibility record, the tag, and a checked draft with notes - ending with one owner check before publishing.
---

# Releasing seestar-copy

A checklist the release preparer runs from start to finish, tag included.
There is one human check: the owner reviews the draft release and its notes,
then approves publication. Any failed check stops the release and goes back
to the owner, and so does an open pull request (step 5).
[Cutting a release](../../../docs/development/releasing.md) has the
mechanics; this file is the order and the checks.

## 1. Pick the commit and the version

- Use a commit on `origin/main` whose CI is green, and write down its full SHA.
- List the changes since the last tag: `git log --oneline vX.Y.Z..<sha>`.
- Choose the version by [the versioning policy](../../../docs/versioning.md).
  A changed or removed command, flag, default, exit code, state file member
  or download is breaking. Before 1.0, a break is a minor release.

## 2. Run the gates

Run these in a clean worktree at that SHA, in `docker.io/library/golang:1.27`
with `--network none`, following
[releasing.md](../../../docs/development/releasing.md):

- [ ] `./scripts/check.sh` passes, along with the release tooling tests;
- [ ] `./scripts/release.sh vX.Y.Z` produces six CLI parts, two Windows app
  parts and the two Windows archives;
- [ ] a second run produces byte-identical CLI parts; rebuilt Linux and
  Windows CLIs match those unpacked from the archives (the apps have no
  reproducibility promise);
- [ ] an artifact-only workflow dispatch with an RC version produces all five
  downloads and the single `SHA256SUMS`, with a signed and notarized DMG
  holding `seestar-copy.app`;
- [ ] the host's CLI part in `dist/parts/` prints the version, commit and
  platform with `--version`, and `licenses` lists
  `github.com/hirochachacha/go-smb2/v2`.

## 3. Check the compatibility record

seestar-copy has no record of its own between releases; each release's
notes carry it.

- [ ] Ask the owner for a device check of the rehearsal's linux/arm64 CLI. It
  is read-only and only the owner runs it; never point the tool at a
  telescope yourself. It covers a `--once` pass into a new destination, the
  copied files' hashes against the device, a second `--once` after removing
  only the test destination's state file (as the reference describes for a
  reset), so it adopts rather than downloads, and a stop with Ctrl+C
  mid-pass.
- [ ] If the build changes after the check (step 1 starts again with a new
  SHA), the check is repeated unless the owner says the change cannot affect
  it.
- [ ] Keep track of what was tested with this build: the date, the
  telescope's model and firmware, the endpoint (direct or through
  seestar-wormhole), what was carried forward from older runs, and what was
  never tested.

## 4. Draft the notes

From the worktree root, set `release_notes=../release-notes-vX.Y.Z.md` and
write the notes to that file, outside the checkout, for users rather than
developers:

1. One summary line.
2. **Changes:** one or two plain sentences each, linking the reference
   section that has the detail. Name any changed log line, though log text
   is not in the contract. Leave out internals such as package names and
   test names.
3. **Breaking changes**, with recovery steps, or "No breaking changes."
4. **Install:** unpack the Linux or Windows archive, or open the Mac DMG.
   Open the desktop app `seestar-copy`, or copy `seestar-copy-cli` onto the
   `PATH` and check `seestar-copy-cli --version`. Point to
   [the desktop app](../../../docs/reference.md#the-desktop-app) for its
   runtime needs and
   [verifying a release](../../../docs/reference.md#verifying-a-release)
   without repeating their commands.
5. **Compatibility record:** the date, model and firmware, a short tested
   list, and what was not tested.
6. The no-affiliation line.

Link docs at the tag (`blob/vX.Y.Z/...`), not at `main`. Keep the tone short
and plain. Verification details are for power users; one line and a link is
enough.

## 5. Tag

- [ ] No pull requests are open:
  `gh pr list --repo zmerlynn/seestar-copy --state open --limit 1000`. If any
  are, list them for the owner and wait for their answer before tagging. An
  open PR may be meant for this release.

```sh
git tag -a vX.Y.Z -m "seestar-copy vX.Y.Z" <sha>
git push origin vX.Y.Z
```

Never move or delete a pushed tag. A broken release is fixed with a new patch
version.

## 6. Check the draft and apply the notes

When the release workflow finishes, check:

- [ ] the run succeeded, and the draft has five downloads plus `SHA256SUMS`;
  an RC tag gives a pre-release draft;
- [ ] the downloaded assets pass `sha256sum --check`, and unpacked Linux and
  Windows CLIs match the local reproducible build; the Mac CLI is
  lipo-combined and signed, so it is not byte-reproducible;
- [ ] each archive holds exactly `seestar-copy` and `seestar-copy-cli`
  (`.exe` on Windows), LICENSE and NOTICE at its root, and the DMG holds
  `seestar-copy.app`, the `Applications` link, the universal
  `seestar-copy-cli`, LICENSE and NOTICE, with LICENSE and NOTICE also in
  `seestar-copy.app/Contents/Resources/`;
- [ ] `gh attestation verify <downloaded-asset> --repo zmerlynn/seestar-copy`
  passes for each of the four archives and the DMG.

Then apply the notes, leaving the release a draft:

```sh
gh release edit vX.Y.Z --draft=true --notes-file "$release_notes"
```

## 7. Owner check: publish

Send the owner the draft link (it changes on every edit, so take it from
`gh release view vX.Y.Z --json url`), with the gate and draft results in a
few lines. Apply their edits to the notes. Publish only on their approval:

```sh
# stable release
gh release edit vX.Y.Z --draft=false --prerelease=false --latest
# release candidate
gh release edit vX.Y.Z-rc.N --draft=false --prerelease=true --latest=false
```

Check the result with `gh release view <tag> --json tagName,isDraft,isPrerelease,url`.
For a stable release, `releases/latest` must now be the new tag; for an RC it
must be unchanged. Then delete the worktree and `dist/`.
