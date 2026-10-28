The GUI fixture runs the embedded page inside Wails on the native CI runners.
It requires Go and the platform's existing window engine. There is no frontend
build, browser launcher, WebDriver, or accessibility driver.

Build and run on a desktop (use an absolute path inside an app bundle on macOS):

```sh
go build -mod=vendor -tags gui,guitest,production -o /absolute/output/gui-fixture ./cmd/seestar-wormhole
go build -mod=vendor -tags guitest -o /absolute/output/gui-harness ./scripts/gui-autotest
/absolute/output/gui-harness -binary /absolute/output/gui-fixture -out /absolute/output/evidence
```

The workflow supplies the native dependencies, bundle, OS theme, screenshot
commands, and outbound-network restrictions. On Windows, build the fixture
with `CGO_ENABLED=0 -ldflags='-H windowsgui'` and use `.exe` output names.
Run only on a disposable desktop or runner. The harness creates and removes
scenario directories below its output directory and disposable state in the OS
temporary directory. Engine profiles stay outside uploaded evidence, including
on failure. Never point it at user state. Failed scenarios print the last 40
lines of each process log, stripping every suffix starting with `#` to keep URL
fragments private. Process output is drained through a shared pipe so a reopened
stderr cannot overwrite earlier stdout diagnostics in the artifact file.

`SEESTAR_GUITEST_OUT` and `SEESTAR_GUITEST_SCENARIO` are private fixture inputs,
set by the harness. The fixture fails without them. Its runner has no network
adapter or proxy listeners; only the existing ephemeral UI listener is used.
The fixture's keychain and browser opener panic if called. Fake credentials use
the reserved `.invalid` domain. Nothing is saved to the OS credential store.

The driver's ExecJS sets input values, dispatches input/change events, and clicks
the page's own buttons. It does not call app.js functions or replace page code.
The fixture marks the Wails ExecJS queue ready at the native navigation-complete
event, since this external HTTP page does not load the full Wails JS runtime.
An opt-in Wails native event acknowledges DOM predicates. Each screenshot waits
for both authenticated server status and matching DOM text/control state.
Transitions wait for the driver, rather than racing the page's polling interval.
Cancellation always joins an eight-second fake release. A screenshot and native
visibility check during that interval assert that the window stays open.

Each scenario has a process deadline, exit-code assertion, listener-closure
check, and fresh native launch using the same state root to check lock release.
The second-instance scenario hides the owner, launches another actual process,
and requires the owner's native Show/Focus state and a successful second exit.
The injected opener panics if the activation path tries to launch a browser.
Windows screenshots bring the app forward, require its native foreground
handle, and capture only its window rectangle; this includes failure shots.
Screenshots, per-process logs, and checkpoint receipts remain in the output.

The workflow exercises Windows WM_CLOSE through CloseMainWindow. macOS/Linux
exercise Wails Window.Close and its WindowClosing hook; this is not a physical
title-bar click. Unix SIGTERM and SIGINT are real process signals. Windows
CTRL_BREAK/console close are explicitly skipped: the fixture uses the GUI PE
subsystem and has no attached console. The Mac Apple-event scenario records the
osascript exit 0 without -128 and requires joined release and app exit 0.
On macOS the harness/osascript sender runs outside sandbox-exec; the fixture
wrapper applies the network sandbox only to the app and its engine descendants.
Sandboxed senders can receive a privilege violation before quit reaches the app;
that refusal is a failure, never an accepted-quit result.
During accepted AppKit termination, OnShutdown blocks the native main thread;
the Apple-event scenario checks the Go release lifetime without native queries
or DOM probes during that join. Its joined receipt is written in OnShutdown,
since AppKit can exit directly after accepting termination.

Dark mode uses the OS preference, with no injected CSS or emulated media query.
On macOS the fixture also sets the native window NSAppearance to Aqua/DarkAqua
for the selected scenario: global defaults written outside sandbox-exec may not
reach AppKit inside it.
The driver checks the dark media query and computed text/background contrast
(at least 3:1 for enabled fields, status, notes, and buttons), and Go checks for
substantial dark background and light glyph pixels in the central screenshot.
Failures log the element ID, computed foreground/background, sampled RGB,
contrast ratio, dark preference, and root luminance. System colors are resolved
through CSS before canvas sampling; a fixture-only raw-message callback reports
diagnostics without enabling the HTTP runtime transport. A failed probe always
also logs native started/evaluated receipts, distinguishing missing execution
from a completed expression whose detailed diagnostic did not arrive. This is a coarse
readability check, not a full accessibility audit.

`-scenarios` selects a comma-separated subset; `dark` repeats the fresh-start,
Stop, and restart sequence. `relaunch` checks page startup and Quit without
starting a runner. The Linux workflow also uses it to record whether the distro
bubblewrap package permits startup with Ubuntu's userns restriction enabled,
before applying the existing CI-only sysctl workaround for the main suite.
The probe result is evidence and does not waive any main-suite failure.

`-verify fixture,production,plain-gui` checks go version metadata and linked
symbols without starting a window. Build these binaries without stripping.
The fixture must contain guitest tags and symbols; both other builds must have
none. This positive control runs on every platform. The normal release scripts
and default build path are unchanged.
