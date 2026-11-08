// Starting the app's page for a test, without a window. UI_TEST_BIN is
// internal/ui's test binary (go test -c ./internal/ui); its
// TestBrowserServer serves the real page and runtime from the state
// folder SEESTAR_WORMHOLE_BROWSER_STATE names, with no account and no
// proxy, and prints the page's private URL. Each app gets its own home and
// config folder in the test's output folder, so copy's state folder,
// home/config/seestar-copy, is fresh unless two apps share a home.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const base = require('@playwright/test');
const { expect } = base;

// The Samba fixture's browse tree, and a port nothing listens on.
const share = '127.0.0.1:14450';
const refused = '127.0.0.1:1';

// env is the environment of a program run with home as its home folder,
// so that copy's state folder is home/config/seestar-copy.
const env = (home) => ({ ...process.env, HOME: home, XDG_CONFIG_HOME: path.join(home, 'config') });

// lan is config.json for a saved route on this network: Test passed and
// Save made it the route, which opens Copy, Browse and Free up space.
const lan = (address = share) => ({
  version: 1, bind: '127.0.0.1', idle: '0', route: 'lan', lan_address: address, route_established: true,
});

// The URL TestBrowserServer prints, as a browser run without a browser
// prints it.
const urlPattern = /http:\/\/127\.0\.0\.1:\d+\/#[0-9a-f]{64}/;

// writePrivate writes a file the app's private storage accepts: its
// folder 0700 and the file 0600, whatever the umask.
function writePrivate(file, value) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.chmodSync(path.dirname(file), 0o700);
  fs.writeFileSync(file, typeof value === 'string' ? value : JSON.stringify(value), { mode: 0o600 });
  fs.chmodSync(file, 0o600);
}

// launch starts the page's server with its state folder in dir and home
// as its home folder (dir if not given). files maps the state folder's
// file names to their content (an object, a function of the app's folder
// for images that returns one, or a string as is); none means a first run.
async function launch(dir, files = {}, home = dir) {
  const state = path.join(dir, 'state');
  const folder = path.join(home, 'Seestar');
  fs.mkdirSync(path.join(home, 'config'), { recursive: true });
  for (let [name, value] of Object.entries(files)) {
    if (typeof value === 'function') value = value(folder);
    if (value !== undefined) writePrivate(path.join(state, name), value);
  }
  const child = spawn(process.env.UI_TEST_BIN, ['-test.run=^TestBrowserServer$', '-test.timeout=0'], {
    env: { ...env(home), SEESTAR_WORMHOLE_BROWSER_STATE: state }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const app = { state, config: path.join(state, 'config.json'), copyFile: path.join(state, 'copy.json'), folder, home, child, log: '' };
  child.stderr.on('data', (b) => (app.log += b));
  app.exited = new Promise((resolve) => child.on('exit', resolve));
  app.url = await new Promise((resolve, reject) => {
    child.stdout.on('data', (b) => {
      app.log += b;
      const m = app.log.match(urlPattern);
      if (m) resolve(m[0]);
    });
    app.exited.then((code) => reject(new Error('the app exited ' + code + ': ' + app.log)));
  });
  return app;
}

// test gives each test startApp, which returns the app's URL, its state
// folder's files and its folder for images; every app is stopped after
// the test and its log attached. startApp(files, home) shares another
// app's home, and so copy's state folder, with a state folder of its own.
const test = base.test.extend({
  startApp: async ({ context }, use, testInfo) => {
    const apps = [];
    await use(async (files, home) => {
      const app = await launch(testInfo.outputPath('app-' + apps.length), files, home);
      apps.push(app);
      return app;
    });
    // Each page's route handlers finish, and the page stops polling, while
    // its app still runs; the apps stop even if that fails.
    try {
      for (const p of context.pages()) {
        await p.unrouteAll({ behavior: 'wait' });
        await p.close();
      }
    } finally {
      for (const app of apps) {
        if (app.child.exitCode === null) app.child.kill('SIGINT');
        await app.exited;
        await testInfo.attach('app log', { body: app.log, contentType: 'text/plain' });
      }
    }
  },
});

const readJSON = (file) => JSON.parse(fs.readFileSync(file, 'utf8'));

// The app page's parts.
const frame = (page) => page.frameLocator('#b-frame');
const browseStatus = (page) => frame(page).locator('#status');
const tab = (page, name) => page.getByRole('tab', { name, exact: true });
const field = (page, label) => page.getByLabel(label, { exact: true });

// openApp loads the page and waits for its first status. The page opens
// on Connection.
async function openApp(page, url) {
  await page.goto(url);
  await expect(page.locator('#quit')).toBeEnabled();
  await expect(page.locator('#version')).not.toHaveText('');
}

// settleStartFocus waits out the page's first focus move. After its first
// status load the page focuses Connect: in the next frame if the window
// has focus, otherwise when the window first gets it, which in WebKit can
// come in the middle of a test's key presses. Connect is enabled by that
// same load, after which the move is due; a focus event on the window
// sends a move still waiting for one, and two frames let it land, so a
// test can then put focus where it wants.
async function settleStartFocus(page) {
  const start = page.locator('#start');
  await expect(start).toBeEnabled();
  await page.evaluate(() => new Promise(done => {
    window.dispatchEvent(new FocusEvent('focus'));
    requestAnimationFrame(() => requestAnimationFrame(done));
  }));
  await expect(start).toBeFocused();
}

// openCopy loads the page and opens the Copy tab once copy's status is in.
async function openCopy(page, url) {
  await openApp(page, url);
  await tab(page, 'Copy').click();
  await expect(page.locator('#f-builtin')).not.toHaveText('');
}

// fakeStatus answers the page's copy status polls with the real status,
// its copy view replaced by view() when that returns one, and its Free up
// space view by free(). A view's activity, if any, replaces copy's
// activity.
async function fakeStatus(page, view, free = () => null) {
  await page.route('**/copy/api/status', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    const f = free();
    if (f) data.free = f;
    const v = view();
    if (v) {
      const { activity, ...copy } = v;
      data.copy = { on: true, contact: 'ok', chip: 'Copying', ...copy };
      if (activity) Object.assign(data, { activity, activity_seq: activity.length });
    }
    await route.fulfill({ response, json: data });
  });
}

// Synthetic Copy views of an active pass, with a long path in flight and
// failed, for screenshots and layouts.
const longPath = 'MyWorks/NGC 7000 North America Nebula/Stacked_210_NGC7000_10.0s_IRCUT_' + '20261029-033119'.repeat(4) + '.fit';
const longDir = longPath.slice(0, longPath.lastIndexOf('/'));
const copyViews = {
  listing: { state: 'listing', title: 'Looking for new files… 37 folders checked.', path: 'Listing ' + longDir + '…' },
  copying: {
    state: 'copying', title: 'Copying 3 of 12 files',
    progress: {
      done: 1200, total: 4000, bytes: '1.2 GB of 4.0 GB', speed: '9.8 MB/s', left: 'About 5 min left', found: 'Found already in your folder: 3',
      flight: [{ path: longPath, percent: 40 }, { path: 'MyWorks/M 31/Stacked_M 31.jpg', percent: 5 }],
    },
  },
  failures: {
    state: 'failures', title: 'Check finished with 1 file not copied. Next check at 21:40.', chip: '1 file not copied',
    summary: 'Copied 11 files (3.9 GB) in 7 min. 1 failed.',
    notice: '1 file couldn\'t be copied. It will be tried again at the next check.',
    failed: [{ path: longPath, reason: "The telescope's file changed size while copying" }],
    activity: [{
      time: '21:35:02', at: '2026-10-29T21:35:02Z', source: 'copy', warn: true,
      text: `copy failed path="${longPath}" err="the connection was reset"`,
    }],
  },
};

// Synthetic Free up space views, with a long path, for layouts.
const freeViews = {
  checking: {
    state: 'checking', title: 'Checking 1,830 files…', chip: 'Checking what to delete', progress: { done: 1200, total: 4000, text: 'Checking your copies: 1.2 GB of 4.0 GB' },
    file: 'Checking ' + longPath + '…',
  },
  preview: {
    state: 'preview', id: 1, title: '2 files (33.2 MB) are copied and safe to delete.',
    sub: 'Only files older than 7 days · your copies checked',
    groups: [
      { folder: longDir, name: longDir, count: 1, bytes: 33200000, size: '33.2 MB', files: [{ path: longPath, size: '33.2 MB' }] },
      { folder: 'MyWorks/M 31', name: 'MyWorks/M 31', count: 1, bytes: 1000, size: '1.0 KB', files: [{ path: 'MyWorks/M 31/Stacked_M 31.jpg', size: '1.0 KB' }] },
    ],
    kept: 'Kept: 1 changed on the telescope',
    kept_list: [{ path: longPath, reason: 'changed on the telescope since it was copied' }],
  },
  deleting: { state: 'deleting', title: 'Deleting 2 of 2…', chip: 'Deleting 2 of 2', progress: { done: 1, total: 2 }, file: 'Last deleted: ' + longPath },
  done: {
    state: 'done', title: 'Deleted 1 file.', text: 'Freed 33.2 MB on the telescope.', kept: '1 file was kept.',
    kept_list: [{ path: longPath, reason: 'kept: changed on the telescope since it was copied' }],
  },
  failed: { state: 'failed', title: `Stopped: the telescope refused to delete ${longPath} (access denied).`, text: 'No files were deleted.' },
};

module.exports = {
  test, expect, share, refused, env, lan, readJSON, frame, browseStatus, tab, field, openApp, settleStartFocus, openCopy, fakeStatus, longPath, copyViews, freeViews,
};
