# Using the UI

seestar-wormhole connects apps on your computer and phone to your remote Seestar.
They share one connection to the telescope.

![Choose Telescope Network or On this network on the Connection tab.](images/gui-setup.png)

## Tabs

The header shows a dot and a line with the connection's state: **Not
connected**, the status while connecting, or **Connected to** your Seestar. The
dot is green only while connected; while connecting, reconnecting, waiting for
a busy telescope or releasing an idle session it shows busy. On
this network it shows the saved address, then **On this network: reachable**
or **Can't reach** the address once **Test** has run. While copying is on, a
chip after the line says how it stands: **Copying**, **Up to date**, how many
files were not copied, or **Copying is on**. A copy once shows **Copying**
while its check runs. With copying off, there is no chip.
While a run is busy, a chip of its own follows: **Previewing copy**, **Checking
what to delete**, **Checking N files again** or **Deleting 3 of 12**. It goes
away when the run ends. Each chip opens its tab, Copy or Free up space. A run's
start is announced once when another tab is open, each time it starts.

Below it are the tabs **Connection**, **Copy**, **Browse**, **Free up space**
and **Activity**. Use the Left and Right arrow keys, Home and End to move
between them. Copy, Browse and Free up space open once you connect remotely
through Telescope Network, or save an address on this network; until then
they are dimmed, show a padlock, and say so. Quit and the version are below every tab.

## Reaching the telescope

**Connection** starts with **Reach the telescope**, and a line under it says
what each choice gives: through Telescope Network, one connection serves
both the Seestar app and the telescope's files; on this network, it serves
only the telescope's files, for Copy, Browse and Free up space.

- **Remotely through Telescope Network** is the setup on the rest of this
  page: the proxy signs in with your Seestar account and connects to your
  Seestar through Telescope Network, and copying will use its file-sharing
  (SMB) port.
- **On this network** is for a Seestar on the same network as this computer.
  When you choose it, or when it shows with no address saved, it scans this
  network the way the phone app does, for about 6 seconds, and lists each
  telescope that answers under **Telescopes on this network**: its name, its
  model, **busy** when it reports as many clients as the phone app allows
  (copying still works), and **saved** for the one saved here. Each address
  is a button that fills it in. **Scan again** repeats the scan. When
  nothing answers, or the telescope's network doesn't pass the scan through,
  enter its [address](copying.md#find-the-telescopes-address) yourself, with an
  optional port (445 by default); its help says
  that Copy, Browse and Free up space read the telescope's files there.
  **Test** checks that it answers file sharing and saves nothing: when it
  passes for an address other than the saved one, it says to **Save**. Test
  also sends the scan request to that address alone, and when a telescope
  answers it adds its name and model.
  **Save** tests it first, unless it has just passed, then checks that it
  still answers (the line under the address says **Checking…**) and saves
  it only if it does; Copy, Browse and Free up space then use it. Otherwise
  the saved route stays.
  On a Mac, when nothing answers at the address, Test or Save also says to
  enable seestar-wormhole in System Settings → Privacy & Security → Local
  Network, which macOS may be blocking; see [Local Network access](copying.md#things-to-know).
  A scan that macOS refuses says so, with the same hint, rather than that
  no telescope answered.
  An address picked from a scan is saved with the telescope's serial
  number. When the router later gives the telescope another address and the
  saved one stops answering, Copy, Browse and Free up space scan for that
  serial, use the telescope's new address and save it; Activity says so,
  naming only the model.
  This route doesn't start the proxy or use your account.

You can't change the choice while a connection is running or while copying or
deleting is running; stop it first. **Connect** saves the Telescope Network route, so
it is refused too while copying or deleting runs on this network.

## Setting up

Under **Use with**, choose who can connect:

- **Phone on this network** is selected the first time. It listens on all
  networks, lets the Seestar app find your telescope, and serves image downloads
  on local port 80. Keep your phone on the same network as this computer. Use a
  trusted network: other devices on it can control your Seestar.
- **This computer only** accepts apps on this computer only. Discovery is off,
  and image downloads use local port 8080.
- **Custom** uses your own settings under **Advanced**. Changing any of them
  selects **Custom**; picking another setup replaces them.

Enter your Seestar account email and password. A Seestar account password
has 8 to 32 characters, and the field says so if yours doesn't. Nothing
connects until you choose **Connect**, and next time your choices are already
filled in.

Your password stays in memory until you quit. To skip typing it next time,
choose **Save password to keychain**; the field then shows **Saved in
keychain**. Your password is never saved in plain text.

To change a saved password, choose **Remove saved password** and type the new
one. If removal fails, unlock your keychain and choose **Retry removal**. See
[keychain troubleshooting](troubleshooting.md#the-os-keychain-is-unavailable).

## Connecting

Choose **Connect**. While a connection is running, the button becomes **Cancel**
or **Stop**. The status tells you what's happening:

| Message | What to do |
|---|---|
| **Connected to your Seestar** | Start using your apps. |
| **Waiting for an app to connect** | Connect an app to a forwarded port. The session starts when one does. |
| **Seestar is occupied. Retrying automatically.** | Disconnect any other remote session, or wait for it to let go. |
| **Seestar isn't reachable. Check this computer's internet connection and make sure Seestar is awake and connected to Wi-Fi. Retrying automatically.** | Check this computer's internet connection and make sure Seestar is awake and connected to Wi-Fi; seestar-wormhole keeps trying. |
| **Connection interrupted. Retrying automatically.** | Wait; seestar-wormhole reconnects on its own. |
| **Waiting before retrying the connection…** | Wait for cleanup and the retry delay. |
| **Stopped. Seestar may still be releasing the session.** | Give it time before using another remote session. |
| **Session release could not be confirmed. If Seestar is occupied, wait and try Connect again.** | Wait, then choose **Connect** again. |
| Could not sign in. Check your Seestar account email and password, then connect again. | Fix your account details and choose **Connect**. |

If a setting is wrong, its field shows what to fix.

Your settings are saved once a connection succeeds. If saving fails, the
connection keeps running and **Retry saving settings** appears.

If the window loses the controls, a warning above the tab contents says so on
every tab and on either route, with the last known state through the account,
and says the proxy may still be running. Reopen seestar-wormhole to reconnect.
Copy, Free up space and Browse are dimmed to show they may be out of date:
Copy's and Free up space's buttons that act on the controls are disabled, and
Browse takes no clicks or keys. If only the status of Copy and Free up space
can't be read, the warning says that instead, and only those two are dimmed.
What you've typed is kept. Connection and Browse come back as soon as the
controls answer; Copy and Free up space come back once their status has been
read again.

## While connected

The setup fields are replaced by a summary of listening, discovery, and
connection timing. Open **Ports forwarded to your Seestar** to see the ports in
use: **Local port** is on this computer, **Seestar port** is on the telescope,
and **Purpose** names the service. You can't edit them while connected.

When discovery is on, **Using the Seestar app** tells you to pick your
telescope from the **LAN** tab of the Seestar app. If it doesn't appear, see
the [phone app guide](phone-app.md). **Copying files** points you to the
**Copy** tab, with **Go to Copy**, and to **Browse**; below that, it shows the
local SMB port for [another file-sharing app](copying.md#copy-with-a-graphical-app)
when that port is on.

Keep seestar-wormhole open while you use the telescope.

## Copying images

**Copy**, **Browse** and **Free up space** copy your images from the
telescope's file share to a folder on this computer, show what is on the
share, and delete what is safely copied. They select files, keep the copy
history and take the folder's locks as `seestar-wormhole-cli`'s [`sync`,
`browse` and `delete`](reference/files-cli.md) do; see
[selection and copy history](reference/selection-and-history.md), which
also says [what each does when the other holds a lock](reference/selection-and-history.md#when-the-other-side-holds-a-lock).
The app keeps its copy settings in its own state
directory, as [`copy.json`](reference/app-settings.md#copyjson-version-1), which the CLI's file commands
read only when given `--profile default`, such as `seestar-wormhole-cli sync --profile default`
(see [use the app's saved settings](reference/files-cli.md#use-the-apps-saved-settings)). Through Telescope Network
they reach the share through the connection itself, so they work with the SMB
port off, and their errors name **Seestar (account connection)**. With
**Connect when an app needs Seestar**, they count as an app: they start the
session when they need it, and it releases once they and every other app are
done. On this network they reach the share at the saved address.

If they can't reach the share, each says why and links to the fix:

| Notice | Fix |
|---|---|
| **Not connected.** | **Go to Connection** and connect. On Copy, while copying repeats, the notice adds **Copying resumes at the next check after you connect.** Scheduled checks wait for the connection; they don't start it. |
| **Save the telescope's address on the Connection tab first.** | **Go to Connection**, choose **On this network**, and save the address. |

### Copy

The first time you open Copy, **Copy settings** is open: choose the folder for
your images, with **Choose…** in the app's window or by typing its full path,
then **Save**. Saving doesn't start copying: a notice above the
buttons says copying starts when you press **Start copying**, and that
**Preview** shows what it would copy first. After that, Copy shows what
copying is doing, with
**Start copying**, **Preview**, **Check now** and **Stop copying**, and below
them a summary of the settings, such as "Copying to ~/Seestar every 5 min,
skipping logs and system files." **Change…** opens the settings, which you
can't change while copying runs:

- **Folder for your images.** Images are copied there in the same folders as
  on the telescope. Each folder has its own copy history.
- **Copy history folder**, empty by default. Empty is the default
  [history location](reference/selection-and-history.md#location), the one
  `seestar-wormhole-cli sync` uses without `--history-dir`, so the app and
  `sync` share one history for the folder; the field shows
  that path as its placeholder. Otherwise type a full path to a folder
  outside the folder for your images, or use **Choose…** in the app's window;
  **Save** refuses a relative path or one inside the images folder with
  **Use a full path to a folder outside the folder for your images, or leave
  it empty for the default.** A folder that leads inside the images folder
  through a symbolic link is saved, and copying and Clear history then refuse it
  without changing anything in the images folder. Nothing
  moves the old history: a different folder starts a fresh one, so files
  still in your images folder are checked and recorded again, files you moved
  out of it are copied again, and Free up space can't use the old history to
  prove that older copies are safe. To share the history with a `sync`, give
  it the same folder as `--history-dir`.
- **Check for new files every** N minutes, 5 by default. Its checkbox, on by
  default, turns the repeating check off to copy once: **Start copying** then
  checks once, copies what is new and stops, and Copy says **Checked once;
  copying is off.** The summary then says "once each time you start". With
  the checkbox off, the minutes are disabled and keep their last valid
  value for when you turn it back on; they never block **Save**.
- **Files at once**: how many files copying runs at once, 1 to 4, 4 by
  default. Through Telescope Network, copying shares the connection with
  the Seestar app and seestar_alp; if they are slow to respond while copying,
  lower it to 1 or 2.
- **Leave out** and **Only copy**: one pattern per line, as `sync`'s
  `--exclude` and `--include`; see [selection](reference/selection-and-history.md#selection).
- **Skip the telescope's logs, scratch and system files**, on by default: the
  [built-in patterns](reference/selection-and-history.md#built-in-patterns).
- **Start copying when the app opens**, off by default. Through Telescope
  Network, copying then starts once the connection is ready; pressing
  **Start copying** before then takes its place. With the check for new
  files off, it copies once each time the app opens.

While copying is off, **Preview** shows what **Start copying** would do, from
one [dry run](reference/files-cli.md#dry-run) with the saved settings: how many
files it would copy and their size, with a row per folder, and how many are
already copied, still being written, or left out. Nothing is copied.
**Cancel** stops a preview that is running; starting, a Save that changes the
folder, the copy history folder or the selection, and Clear history discard
it.

While copying is on, Copy holds the folder's
[locks](reference/selection-and-history.md#locks) as `sync` does. If a
`sync`, a `delete --force` or another copy of the app holds one, Copy says
**Another copy or delete is using this folder.**: when you start copying,
copying stays off, so stop the other one first; at a later check, that check
is skipped and Copy tries again at the next one. See
[when the other side holds a lock](reference/selection-and-history.md#when-the-other-side-holds-a-lock).

**Copy history**, collapsed under the settings, shows the path of the saved
folder's [state file](reference/selection-and-history.md#copy-history) in the saved copy
history folder, which `sync` uses too with the same `--history-dir`. While
the folder field or the copy history folder field holds one not yet saved,
it says to save first. **Clear history…** deletes that state file after a confirmation, so the
app forgets what it copied to the folder. Files still in the folder are kept
and adopted on the next pass; files moved out of it are copied again. Clear
history is unavailable while copying is on, and refuses while Free up space
is checking or deleting, or while a `sync` holds the folder's lock.

### Browse

Browse is `seestar-wormhole-cli browse`'s page for the share, with the saved
folder's [statuses](reference/files-cli.md#statuses). What the settings leave out
shows as [`left out`](reference/files-cli.md#left-out-in-browse). After a Save
that changes the folder or the selection, or a change of how the telescope
is reached, Browse loads again at the share's top when it is next shown, so
an error from before goes without a retry. Browse opens once a route is
saved: until Copy's folder is saved it lists the telescope's files without
statuses, and the first Save loads it again with them. In the app's
window, a file's **Save** asks where to save it; in a browser, it downloads.
A save into Copy's saved folder, or a folder inside it, takes the folder's
[folder lock](reference/selection-and-history.md#the-folder-lock) until the file is in place:
while a check or a delete holds it, the save says **Another copy or delete
is using this folder. Try saving it again.** and saves nothing. A file named
`.seestar-wormhole.lock`, in any letter case, can't be saved at the top of
that folder: **That name is kept for the copy lock. Choose another name or
folder.** A save anywhere else takes no lock.

### Free up space

Until Copy's folder is saved, Free up space says to choose it, with **Go to
Copy**. **Preview what to delete** lists, per folder, the files that are copied and
safe to delete, as `delete` [proves it](reference/files-cli.md#proof-modes), and
nothing is deleted until you confirm. Uncheck a folder to keep it. **Only
files older than** N days (7 by default; 0 means any age) keeps recent
sessions on the telescope, and **My copies have moved or the drive is
offline** checks only the app's record of each copy, as `--trust-state`
does. Above the options, **Copies in:** names Copy's saved folder, whose
copies the check proves; with that option on, **Copy history for:** names the
folder whose history it uses instead. **Go to Copy** beside it is where to
change the folder. **Keep on the telescope** and **Only free up** take one pattern per
line, as `delete`'s `--exclude` and `--include`; see [selection](reference/selection-and-history.md#selection):
a file that matches Keep on the telescope is never deleted, and when Only free
up is not empty, only files that match it are. They are separate from Copy's
**Leave out** and **Only copy**, which don't change what is deleted.
**Always kept**, beside them, lists what is never deleted: the
[built-in patterns](reference/selection-and-history.md#built-in-patterns),
which `delete` leaves out by default too. A Copy Save that changes the folder discards a
preview, and so does a change of how the telescope is reached, which also
clears a check that couldn't finish. **Delete N files…** asks once more. The delete then lists the checked
folders again, and only those, saying **Checking N files again before
deleting…**, and proves each file again just before it removes it: a file
that changed or went away since the preview is kept. **Stop** ends a delete
that is running. The delete holds the folder's
[folder lock](reference/selection-and-history.md#the-folder-lock) from before it lists the
folders until its last removal, and runs between Copy's checks: while a
check, a `sync` or another delete holds the lock, it stops at once with
**Another copy or delete is using this folder.** and deletes nothing. A
preview takes no lock.

## Stopping and quitting

**Stop** releases the telescope and leaves the window ready to connect again.
**Quit** stops copying and the connection and exits: browsing ends first,
then copying, a preview or a delete stops and is waited for, then the
connection, then the control page. A file partly copied is copied again next
time. Closing the window does the same and waits for the release. Logging out or shutting down also stops the
connection first, as far as your system allows.

Opening seestar-wormhole again with the same state directory brings the
existing window forward. If `seestar-wormhole-cli proxy` is already using the
same ports, stop it first.

## Telescope selection

The **Telescope** picker sits below the account fields. **Find on my account** is the
default: with one telescope on the account, Connect selects it. A saved Serial
and Model pair appears as a selected entry. If it is absent from the current
account's received list, its label is **Saved: {model} {serial}**, using the same
escaping and capping as received labels. When that is the only identity entry,
the picker help reads **Choose “Find on my account”, then Connect to load other telescopes.**
This replaces the discovery help. A received match has its normal label and
appears only once.

If the account has several telescopes, Connect shows **Choose a telescope**
and adds each model and full serial number to the picker. Its help reads
**Your account has several telescopes. Choose one, then Connect.** The picker
receives focus once, with Connect next in keyboard order. Choosing an entry
fills the exact account pair and shows **Ready to connect**; choose Connect
to start the session. The empty option now reads **Choose a telescope…**;
Connect with it still selected in this step keeps
the picker focused without another account request. Outside this step, including
a changed account, it reverts to **Find on my account**. Its associated help reads **Connect uses your only telescope, or asks which one if there are several.**
The selection-step help replaces that line; only one picker help line appears at a time.

Serials must be 1-64 printable ASCII bytes (0x21-0x7E; no spaces, controls, or non-ASCII). This bound also applies to saved settings and received picker choices. An invalid saved serial refuses loading without rewriting the file. See the [connection command reference](reference/connection-cli.md#device-selection) for selection rules.

**Enter manually…** reveals **Serial number** and **Model (optional)** below
the picker for manual entry. Connect follows the revealed fields in visual and
keyboard order; Tab from the picker reaches Serial next. In manual entry, an
empty Serial shows **Enter a serial number.** Connect validates it locally without
an account request. The instruction and validation error are associated with Serial. A usable serial
shows **Ready to connect**; Model remains optional. Choosing another entry hides these fields and
fills its exact pair; Find on my account clears both. The received list stays in memory
for this app run, including when the controls page is reopened. Only Serial and
Model are saved in the existing settings format. A selection failure for a
chosen pair says **Choose “Find on my account” to reload telescopes, or check Serial and Model.**
Absence from the catalog alone does not invalidate a saved pair; the detail
fallback still applies. The received telescope list is included in Recent events, the private log, and Copy diagnostic summary; see [Activity](#activity).

After selection, status includes the escaped model and full serial. Valid serials preserve printable ASCII verbatim without truncation. Only the model is capped at 64 display characters. For example: **Connected to Seestar S50 Pro {serial}**.
An empty model uses **Seestar {serial}**. Recent events and Copy diagnostic
summary carry the same status text.
Stop is the only action below the running status. To switch telescopes, choose
Stop, choose **Find on my account** or another picker entry, then Connect.
Several telescopes still enter the existing selection step. See
[device selection](reference/connection-cli.md#device-selection).

## Advanced

### Timing and idle

Under **Connection timing**:

- **Keep connected** starts the session when you connect and holds it until you
  stop.
- **Connect when an app needs Seestar** waits for an app.
  **Release after apps disconnect (minutes)** sets how long to wait after the
  last app leaves. The next app starts the session again.

### Listening and discovery

Under **Listen on**, choose **All networks**, **This computer only**, or
**Other…**. For **Other…**, enter this computer's IPv4 address in **Local
network address**.

**Let the Seestar app find your Seestar** answers discovery requests on your
network. It needs **All networks** or a local network IPv4 address. See
[LAN discovery](reference/connection-cli.md#lan-discovery) for network requirements and limits.

### Services and ports

The **Services** table has ten fixed rows. Leave a **Local port** empty to turn
that service off. Each enabled local port must be unique and between 1 and
65535. The phone setup maps local port 80 to Seestar port 80; the computer-only
setup uses local port 8080 instead. **File sharing for other apps (SMB)** uses
local port 4445 for Seestar port 445. It is for Finder, rclone, other SMB apps
and `seestar-wormhole-cli`'s file commands, including their `--profile default`
endpoint through the account: Copy, Browse and Free up space don't need it. For other mappings, use the
[command line](reference/connection-cli.md#port-mappings).

## Activity

The **Activity** tab lists the connection's recent events, copying's and
deleting's activity, and Browse's failures and recoveries in one list, newest
first, each tagged **Connection**, **Copy**, **Browse** or **Free up space**.
**Details and logs** holds your connection and copy
settings as JSON, error codes, and the paths of the private log and the
settings files, `config.json` and `copy.json`.

**Copy diagnostic summary** copies one JSON object with the program name,
version, error code, connection and copy settings, the events, the current
failures, and the log's path. The connection settings, and the failure and
file-tool events, leave out addresses, paths, file names, credentials and
tokens. The summary still includes your telescopes' full serial numbers (in
connection and account-catalog events), your copy folder and history folder,
your patterns, and the private log's path. Remove what you don't want to
share before you post it.
Lifecycle events include the selected telescope model and full serial with the
same representation and model display cap as status. Each Connect that reads the account
catalog also records its telescope list, even for a single telescope:
`Account telescopes: {telescope}, …`. Unproven lists use
`Account telescopes (list may be incomplete): {telescope}, …`; empty lists
read `Account telescopes: none`. Long lists end with `… and N more`.
The private log includes an INFO `device_catalog` record with the pairs, count,
and completeness; see the [log reference](reference/connection-cli.md#logs).

Unchanged status polls preserve the text and nodes in Details and the service
tables so selected text stays selected. Changed content updates on the next poll.

The private logs use fixed message templates and categories, UTC timestamps, and
classified failure phases and reasons, with no raw protocol traces.

If seestar-wormhole fails to start, your system shows the error. On Windows,
the error says when you need the WebView2 Runtime.

## Running without the window

Once the UI has saved your settings, `seestar-wormhole-cli proxy --config` can
use them without a window. See [Use saved settings headless](reference/connection-cli.md#use-saved-settings-headless)
for password sources, overrides, and service setup.

## Details for admins

Where the app keeps its settings, the formats of `config.json` and
`copy.json`, file permissions, the control page and the window engine's data
are in [App settings and internals](reference/app-settings.md):

- <a id="where-settings-live"></a>[Where settings live](reference/app-settings.md#where-settings-live)
- <a id="configjson-version-1"></a>[config.json version 1](reference/app-settings.md#configjson-version-1)
- <a id="copyjson-version-1"></a>[copy.json version 1](reference/app-settings.md#copyjson-version-1)
- <a id="file-permissions"></a>[File permissions](reference/app-settings.md#file-permissions)
- <a id="the-control-page"></a>[The control page](reference/app-settings.md#the-control-page)
- <a id="window-limits"></a>[Window limits](reference/app-settings.md#window-limits)
- <a id="engine-data"></a>[Engine data](reference/app-settings.md#engine-data)
