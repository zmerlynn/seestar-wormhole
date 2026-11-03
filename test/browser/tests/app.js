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

// launch writes settings (an object, or a string as is; no file if
// undefined) to app.json in dir and starts the app on it.
async function launch(dir, settings) {
  const file = path.join(dir, 'config', 'app.json');
  const folder = path.join(dir, 'Seestar');
  fs.mkdirSync(path.dirname(file), { recursive: true });
  if (settings !== undefined) {
    fs.writeFileSync(file, typeof settings === 'string' ? settings : JSON.stringify(settings));
  }
  const child = spawn(process.env.APP_BIN, ['--settings', file], {
    env: { ...process.env, HOME: dir, XDG_CONFIG_HOME: path.join(dir, 'config') },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const app = { file, folder, home: dir, child, log: '' };
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
// log attached.
const test = base.test.extend({
  // eslint-disable-next-line no-empty-pattern
  startApp: async ({}, use, testInfo) => {
    const apps = [];
    await use(async (settings) => {
      const app = await launch(testInfo.outputPath('app-' + apps.length), settings);
      apps.push(app);
      return app;
    });
    for (const app of apps) {
      app.child.kill('SIGINT');
      await app.exited;
      await testInfo.attach('app log', { body: app.log, contentType: 'text/plain' });
    }
  },
});

const readSettings = (file) => JSON.parse(fs.readFileSync(file, 'utf8'));

// The app page's parts.
const frame = (page) => page.frameLocator('#b-frame');
const browseStatus = (page) => frame(page).locator('#status');
const tab = (page, name) => page.getByRole('tab', { name, exact: true });
const field = (page, label) => page.getByLabel(label, { exact: true });

// openApp loads the page and waits for it to show Setup or the tabs.
async function openApp(page, url) {
  await page.goto(url);
  await expect(page.locator('#loading')).toBeHidden();
}

module.exports = { test, expect, share, refused, readSettings, frame, browseStatus, tab, field, openApp };
