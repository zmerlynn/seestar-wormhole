// Starting the browser-only seestar-copy app for a test. APP_BIN is the
// built cmd/seestar-copy. Each app gets its own home and config folder in
// the test's output folder, so the state folder is fresh.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const base = require('@playwright/test');
const { expect } = base;

// The Samba fixture's browse tree, and a port nothing listens on.
const share = '127.0.0.1:14450';
const refused = '127.0.0.1:1';

// env is the environment of a program run with home as its home folder,
// so that its state folder is home/config/seestar-copy.
const env = (home) => ({ ...process.env, HOME: home, XDG_CONFIG_HOME: path.join(home, 'config') });

// launch writes settings (an object, a function of the app's folder that
// returns one, or a string as is; no file if undefined) to app.json in dir
// and starts the app on it, with home as its home folder (dir if not given).
async function launch(dir, settings, home = dir) {
  const file = path.join(dir, 'config', 'app.json');
  const folder = path.join(home, 'Seestar');
  if (typeof settings === 'function') settings = settings(folder);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  if (settings !== undefined) {
    fs.writeFileSync(file, typeof settings === 'string' ? settings : JSON.stringify(settings));
  }
  const child = spawn(process.env.APP_BIN, ['--settings', file], { env: env(home), stdio: ['ignore', 'pipe', 'pipe'] });
  const app = { file, folder, home, child, log: '' };
  child.stderr.on('data', (b) => (app.log += b));
  app.exited = new Promise((resolve) => child.on('exit', resolve));
  app.url = await new Promise((resolve, reject) => {
    let out = '';
    child.stdout.on('data', (b) => {
      out += b;
      if (out.includes('\n')) resolve(out.split('\n')[0]);
    });
    app.exited.then((code) => reject(new Error('the app exited ' + code + ': ' + app.log)));
  });
  return app;
}

// test gives each test startApp, which returns the app's URL, settings
// file and folder for images; every app is stopped after the test and its
// log attached. startApp(settings, home) shares another app's home, and so
// its state folder.
const test = base.test.extend({
  startApp: async ({ context }, use, testInfo) => {
    const apps = [];
    await use(async (settings, home) => {
      const app = await launch(testInfo.outputPath('app-' + apps.length), settings, home);
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
        app.child.kill('SIGINT');
        await app.exited;
        await testInfo.attach('app log', { body: app.log, contentType: 'text/plain' });
      }
    }
  },
});

const readSettings = (file) => JSON.parse(fs.readFileSync(file, 'utf8'));

// The app page's parts.
const frame = (page) => page.frameLocator('#b-frame');
const browseStatus = (page) => frame(page).locator('#status');
const tab = (page, name) => page.getByRole('tab', { name, exact: true });
const field = (page, label) => page.getByLabel(label, { exact: true });

// openApp loads the page and waits for it to show the tabs.
async function openApp(page, url) {
  await page.goto(url);
  await expect(page.locator('#loading')).toBeHidden();
}

// fakeStatus answers the page's status polls with the real status, its
// copy view replaced by view() when that returns one, and its Free up
// space view by free(). A view's activity, if any, replaces Recent
// activity.
async function fakeStatus(page, view, free = () => null) {
  await page.route('**/app/api/status', async (route) => {
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
const copyViews = {
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
    activity: [{ time: '21:35:02', warn: true, text: `copy failed path="${longPath}" err="the connection was reset"` }],
  },
};

// Synthetic Free up space views, with a long path, for layouts.
const longDir = longPath.slice(0, longPath.lastIndexOf('/'));
const freeViews = {
  preview: {
    state: 'preview', id: 1, title: '2 files (33.2 MB) are copied and safe to delete.',
    sub: 'Only files older than 7 days · your copies checked',
    groups: [
      { folder: longDir, name: longDir, bytes: 33200000, size: '33.2 MB', files: [{ path: longPath, size: '33.2 MB' }] },
      { folder: 'MyWorks/M 31', name: 'MyWorks/M 31', bytes: 1000, size: '1.0 KB', files: [{ path: 'MyWorks/M 31/Stacked_M 31.jpg', size: '1.0 KB' }] },
    ],
    kept: 'Kept: 1 changed on the telescope',
    kept_list: [{ path: longPath, reason: 'changed on the telescope since it was copied' }],
  },
  deleting: { state: 'deleting', title: 'Deleting 2 of 2…', progress: { done: 1, total: 2 }, file: 'Last deleted: ' + longPath },
  done: {
    state: 'done', title: 'Deleted 1 file.', text: 'Freed 33.2 MB on the telescope.', kept: '1 file was kept.',
    kept_list: [{ path: longPath, reason: 'kept: changed on the telescope since it was copied' }],
  },
  failed: { state: 'failed', title: `Stopped: the telescope refused to delete ${longPath} (access denied).`, text: 'No files were deleted.' },
};

module.exports = {
  test, expect, share, refused, env, readSettings, frame, browseStatus, tab, field, openApp, fakeStatus, longPath, copyViews, freeViews,
};
