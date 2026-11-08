# <img src="../internal/icon/icon.svg" width="48" height="48" alt=""> seestar-wormhole documentation

Start by choosing how to reach your Seestar. Then use the app or command-line tools to control it, copy images, browse files, or free up space.

## Install and choose a connection

- [Install](../README.md#install) the app and the command-line tools.
- [Your account and your site](../README.md#before-you-start-your-account-and-your-site):
  when you need a Seestar account, and why the telescope keeps its own site.
- [Reaching the telescope](ui.md#reaching-the-telescope): Telescope Network
  through your Seestar account, for the Seestar app and the telescope's
  files, or On this network, for the files of a telescope on the same
  network as this computer.
- [Find the telescope's address](copying.md#find-the-telescopes-address) for
  On this network.
- [Sign in from the command line](../README.md#sign-in) to run the proxy
  without the app.

## Use the app

- [Using the UI](ui.md): set up, connect, and what each tab shows.
- [Telescope selection](ui.md#telescope-selection) when your account has
  more than one telescope.
- [Activity](ui.md#activity) and [stopping and quitting](ui.md#stopping-and-quitting).

## Use the phone or control clients

- [Use the phone app](phone-app.md) through seestar-wormhole.
- [seestar_alp and other clients](clients.md).

## Copy, browse, and free up space

- [Copy images](copying.md): in the app, from the command line, with rclone
  or with another SMB app.
- In the app: [Copy](ui.md#copy), [Browse](ui.md#browse) and
  [Free up space](ui.md#free-up-space).
- From the command line: `sync`, `find`, `browse`, `delete` and `scan` in
  the [file command reference](reference/files-cli.md).
- [Selection and copy history](reference/selection-and-history.md): which
  files are copied, where the copy history is kept, and how the app and the
  commands share a folder.

## Keep it running

- [Keeping it running](always-on.md): the proxy as a service, in a
  container, or on Windows.
- [Keep copying without the app](always-on.md#keep-copying-without-the-app):
  `sync` as a second service.

## Troubleshoot

- [Troubleshooting](troubleshooting.md), including
  [filing an issue](troubleshooting.md#filing-an-issue).
- [Known issues and accepted limits](known-issues.md).

## References and tested coverage

- [References](reference/README.md):
  - [Connection command reference](reference/connection-cli.md)
  - [File command reference](reference/files-cli.md)
  - [App settings and internals](reference/app-settings.md)
  - [Selection and copy history](reference/selection-and-history.md)
- [Status](status.md): what has been tested, on which platforms.
- [How the proxy speaks the protocol](protocol.md).
- [Versioning](versioning.md): what a version number promises.
- Moved pages, kept for old links: [Command reference](reference.md) and
  [Copy command reference](copy-reference.md).
- [Development](https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/development/README.md),
  in the source repository.
