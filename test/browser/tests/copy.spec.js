// The app's Copy tab: real passes from the Samba fixture's browse tree,
// Stop and Check now, Folder in use from the CLI and from a second app, and
// the status, progress and focus rules with synthetic states.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { test, expect, share, env, tab, field, openApp, fakeStatus, longPath, copyViews } = require('./app');

// The browse tree has 7 files, all older than the first pass's age.
const copiedAll = /^Copied 7 files \(\d.* in .*\.$/;
const upToDate = /^Up to date\. Next check at \d\d:\d\d\.$/;
const off = 'Copying is off.';
const inUse = 'Another copy is already using this folder.';

// copying is saved settings for the fixture that copies to the app's folder.
const copying = (start) => (folder) => ({ version: 1, address: share, folder, start_copying: start });

const title = (page) => page.locator('#c-title');
const button = (page, name) => page.getByRole('button', { name, exact: true });
const passes = (page) => page.locator('#c-log li', { hasText: /^\S+ pass done( |$)/ });

test('Start copying after the first Save runs a pass, and Settings are locked while copying is on', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await field(page, 'Telescope address').fill(share);
  await field(page, 'Folder for your images').fill(app.folder);
  await button(page, 'Save').click();

  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(title(page)).toHaveText(off);
  await button(page, 'Start copying').click();
  await expect(title(page)).toHaveText(upToDate, { timeout: 30000 });
  await expect(title(page)).toBeFocused();
  await expect(page.locator('#c-live')).toHaveText(upToDate);
  await expect(page.locator('#c-summary')).toHaveText(copiedAll);
  await expect(page.locator('#hchip')).toHaveText('Up to date');
  await expect(page.locator('#hdot')).toHaveText('Reachable');
  await expect(button(page, 'Check now')).toBeVisible();
  await expect(button(page, 'Stop copying')).toBeVisible();
  await expect(button(page, 'Start copying')).toBeHidden();
  await expect(page.locator('#c-help')).toBeVisible();
  for (const f of ['00-notes.txt', 'empty.txt', 'MyWorks/M 31/Stacked_M 31.fit', 'MyWorks/M 31/Notes/log.txt']) {
    expect(fs.existsSync(path.join(app.folder, f)), f).toBe(true);
  }
  expect(fs.readFileSync(path.join(app.folder, 'MyWorks/M 31/Stacked_M 31.fit')).length).toBe(5760);
  await page.getByText('Recent activity').click();
  await expect(passes(page)).toHaveCount(1);
  await expect(page.locator('#c-log li', { hasText: 'copying started' })).toHaveCount(1);

  // Settings can't change, nor the history clear, while copying is on;
  // its Stop copying unlocks them.
  await tab(page, 'Settings').click();
  await expect(page.locator('#s-locked')).toBeVisible();
  await expect(field(page, 'Telescope address')).toBeDisabled();
  await expect(button(page, 'Clear history…')).toBeDisabled();
  await expect(page.locator('#h-clip')).toBeEnabled();
  await page.locator('#s-stop').click();
  await expect(page.locator('#s-locked')).toBeHidden();
  await expect(field(page, 'Telescope address')).toBeEnabled();
  await expect(field(page, 'Telescope address')).toBeFocused();
  await expect(page.locator('#hchip')).toHaveText('Copying is off');
  await tab(page, 'Copy').click();
  await expect(title(page)).toHaveText(off);
  // The last pass's summary stays.
  await expect(page.locator('#c-summary')).toHaveText(copiedAll);
});

test('Clear history forgets the folder, and the next pass copies nothing again', async ({ page, startApp }) => {
  const app = await startApp(copying(true));
  await openApp(page, app.url);
  await expect(title(page)).toHaveText(upToDate, { timeout: 30000 });
  await expect(page.locator('#c-summary')).toHaveText(copiedAll);
  await button(page, 'Stop copying').click();
  await expect(title(page)).toHaveText(off);

  await tab(page, 'Settings').click();
  const history = await page.locator('#h-path').textContent();
  expect(fs.existsSync(history)).toBe(true);
  // Cancel keeps it; focus goes back to the button.
  await button(page, 'Clear history…').click();
  await expect(page.getByRole('dialog', { name: 'Clear the copy history?' })).toContainText('No images will be deleted.');
  await page.keyboard.press('Escape');
  await expect(button(page, 'Clear history…')).toBeFocused();
  expect(fs.existsSync(history)).toBe(true);

  await button(page, 'Clear history…').click();
  await button(page, 'Clear history').click();
  await expect(page.locator('#h-result')).toHaveText('Copy history cleared.');
  expect(fs.existsSync(history)).toBe(false);

  // The files still in the folder are adopted, not copied again.
  await tab(page, 'Copy').click();
  await button(page, 'Start copying').click();
  await expect(title(page)).toHaveText(upToDate, { timeout: 30000 });
  await expect(page.locator('#c-summary')).toHaveText('No files were copied. 7 were already in your folder.');
  expect(fs.existsSync(history)).toBe(true);
});

test('Check now runs another pass, and Stop and Start move focus to the heading', async ({ page, startApp }) => {
  const app = await startApp(copying(true));
  await openApp(page, app.url);
  await expect(title(page)).toHaveText(upToDate, { timeout: 30000 });
  await page.getByText('Recent activity').click();
  await expect(passes(page)).toHaveCount(1);

  await button(page, 'Check now').click();
  await expect(passes(page)).toHaveCount(2);
  await expect(title(page)).toHaveText(upToDate);
  await expect(page.locator('#c-summary')).toHaveText('No files were copied.');

  await button(page, 'Stop copying').click();
  await expect(title(page)).toHaveText(off);
  await expect(title(page)).toBeFocused();
  await expect(page.locator('#c-live')).toHaveText(off);
  await expect(button(page, 'Start copying')).toBeVisible();
  await expect(button(page, 'Check now')).toBeHidden();
  await expect(page.locator('#hchip')).toHaveText('Copying is off');

  await button(page, 'Start copying').click();
  await expect(title(page)).toHaveText(upToDate);
  await expect(title(page)).toBeFocused();
  await expect(passes(page)).toHaveCount(3);
});

test('the CLI copying to the same folder shows Folder in use until it stops', async ({ page, startApp }, testInfo) => {
  const home = testInfo.outputPath('cli');
  const folder = path.join(home, 'Seestar');
  fs.mkdirSync(home, { recursive: true });
  const cli = spawn(process.env.CLI_BIN, ['sync', '--endpoint', share, '--dest', folder], { env: env(home), stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  const exited = new Promise((resolve) => cli.on('exit', resolve));
  const passed = new Promise((resolve) => {
    const read = (b) => {
      log += b;
      if (log.includes('pass done')) resolve();
    };
    cli.stdout.on('data', read);
    cli.stderr.on('data', read);
  });
  try {
    await Promise.race([passed, exited.then((code) => { throw new Error('the CLI exited ' + code + ': ' + log); })]);

    const app = await startApp(copying(true), home);
    await openApp(page, app.url);
    await expect(title(page)).toHaveText(inUse);
    await expect(page.locator('#c-text')).toHaveText("It's in another seestar-copy window or seestar-copy-cli. Stop that copy, then start copying here.");
    await expect(page.locator('#c-box')).toHaveClass('notice err');
    await expect(page.locator('#c-live')).toHaveText(inUse);
    await expect(page.locator('#hchip')).toHaveText('Copying is off');
    await expect(button(page, 'Start copying')).toBeVisible();
    await expect(button(page, 'Stop copying')).toBeHidden();
    // Settings stay open: nothing is copying here.
    await tab(page, 'Settings').click();
    await expect(page.locator('#s-locked')).toBeHidden();
    await tab(page, 'Copy').click();

    cli.kill('SIGINT');
    await exited;
    await button(page, 'Start copying').click();
    await expect(title(page)).toHaveText(upToDate, { timeout: 30000 });
    // The CLI copied everything into the same state.
    await expect(page.locator('#c-summary')).toHaveText('No files were copied.');
  } finally {
    cli.kill('SIGINT');
    await exited;
    await testInfo.attach('CLI log', { body: log, contentType: 'text/plain' });
  }
});

test('a second app on the same folder shows Folder in use until the first stops', async ({ page, context, startApp }) => {
  const first = await startApp(copying(true));
  await openApp(page, first.url);
  await expect(title(page)).toHaveText(upToDate, { timeout: 30000 });

  const second = await startApp(copying(true), first.home);
  const other = await context.newPage();
  await openApp(other, second.url);
  await expect(title(other)).toHaveText(inUse);
  await expect(button(other, 'Start copying')).toBeVisible();
  await expect(other.locator('#hchip')).toHaveText('Copying is off');

  await button(page, 'Stop copying').click();
  await expect(title(page)).toHaveText(off);
  await button(other, 'Start copying').click();
  await expect(title(other)).toHaveText(upToDate, { timeout: 30000 });
  await expect(other.locator('#c-summary')).toHaveText('No files were copied.');
  await expect(title(other)).toBeFocused();
});

test('the status region, bars and focus follow the stage, not each tick', async ({ page, startApp }) => {
  const app = await startApp(copying(false));
  let v = null;
  await fakeStatus(page, () => v);
  await openApp(page, app.url);
  await expect(title(page)).toHaveText(off);
  const live = page.locator('#c-live');
  const busy = page.locator('#c-busy');
  const bar = page.locator('#c-bar');
  // The Copy tab has one status region.
  await expect(page.locator('#p-copy [role=status]')).toHaveCount(1);
  await expect(live).toHaveAttribute('role', 'status');

  // Connecting and listing have an indeterminate bar with a label.
  v = { state: 'connecting', title: 'Connecting to ' + share + '…', contact: 'connecting' };
  await expect(live).toHaveText('Connecting to ' + share + '…');
  await expect(busy).toBeVisible();
  await expect(busy).toHaveAttribute('aria-label', 'Connecting');
  expect(await busy.evaluate((el) => el.hasAttribute('value') || el.hasAttribute('max'))).toBe(false);
  await expect(page.locator('#c-copying')).toBeHidden();
  await expect(page.locator('#hdot')).toHaveText('Connecting…');

  v = { state: 'listing', title: 'Looking for new files…' };
  await expect(live).toHaveText('Looking for new files…');
  await expect(busy).toHaveAttribute('aria-label', 'Looking for new files');
  // Folder counts within listing change the heading, not the status region.
  v = { state: 'listing', title: 'Looking for new files… 3 folders checked.' };
  await expect(title(page)).toHaveText('Looking for new files… 3 folders checked.');
  v = { state: 'listing', title: 'Looking for new files… 4 folders checked.' };
  await expect(title(page)).toHaveText('Looking for new files… 4 folders checked.');
  await expect(live).toHaveText('Looking for new files…');

  // Copying has a determinate bar with value and max, and per-file lines
  // outside the status region.
  const progress = (done, n) => ({
    state: 'copying', title: `Copying ${n} of 4 files`,
    progress: {
      done, total: 4000, bytes: `${done / 1000} kB of 4.0 kB`, speed: '1.2 MB/s', left: 'Less than a minute left',
      found: 'Found already in your folder: 1', flight: [{ path: 'MyWorks/M 31/Stacked_M 31.fit', percent: 25 }],
    },
  });
  v = progress(1000, 1);
  await expect(title(page)).toHaveText('Copying 1 of 4 files');
  await expect(live).toHaveText('Copying 1 of 4 files');
  await expect(busy).toBeHidden();
  await expect(bar).toBeVisible();
  await expect(bar).toHaveAttribute('aria-label', 'Copied so far');
  await expect(bar).toHaveJSProperty('max', 4000);
  await expect(bar).toHaveJSProperty('value', 1000);
  await expect(page.locator('#c-bytes')).toHaveText('1 kB of 4.0 kB');
  await expect(page.locator('#c-speed')).toHaveText('1.2 MB/s');
  await expect(page.locator('#c-left')).toHaveText('Less than a minute left');
  await expect(page.locator('#c-found')).toHaveText('Found already in your folder: 1');
  await expect(page.locator('#c-flight li')).toHaveText(['MyWorks/M 31/Stacked_M 31.fit25%']);
  await expect(page.locator('#hchip')).toHaveText('Copying');

  // Ticks within copying change the heading and bar, not the status region.
  v = progress(3000, 3);
  await expect(title(page)).toHaveText('Copying 3 of 4 files');
  await expect(bar).toHaveJSProperty('value', 3000);
  await expect(live).toHaveText('Copying 1 of 4 files');

  // A pass with failures: the notice and Show files, and a warning chip.
  v = {
    state: 'failures', title: 'Check finished with 1 file not copied. Next check at 21:40.', chip: '1 file not copied',
    summary: 'Copied 3 files (3.0 kB) in 2 s. 1 failed.',
    notice: '1 file couldn\'t be copied. It will be tried again at the next check.',
    failed: [{ path: 'MyWorks/M 31/Stacked_M 31.fit', reason: 'the connection was reset' }],
  };
  await expect(live).toHaveText('Check finished with 1 file not copied. Next check at 21:40.');
  await expect(bar).toBeHidden();
  await expect(page.locator('#c-failed')).toBeVisible();
  await expect(page.locator('#hchip')).toHaveText('1 file not copied');
  await expect(page.locator('#hchip')).toHaveClass('chip warn');
  await page.getByText('Show files').click();
  await expect(page.locator('#c-files li')).toHaveText(['MyWorks/M 31/Stacked_M 31.fit — the connection was reset']);

  // Background updates never move focus.
  await tab(page, 'Copy').focus();
  v = { state: 'unreachable', title: `Can't reach the telescope at ${share}.`, contact: 'err' };
  await expect(live).toHaveText(`Can't reach the telescope at ${share}.`);
  await expect(page.locator('#c-failed')).toBeHidden();
  await expect(page.locator('#hdot')).toHaveText("Can't reach");
  await expect(tab(page, 'Copy')).toBeFocused();
});

for (const colorScheme of ['light', 'dark']) {
  test(`Copy tab screenshots in ${colorScheme}`, async ({ page, startApp }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    const shot = (name) => page.screenshot({
      fullPage: true,
      path: path.join(__dirname, '..', 'screenshots', `${testInfo.project.name}-${colorScheme}-copy-${name}.png`),
    });
    const app = await startApp(copying(false));
    let v = null;
    await fakeStatus(page, () => v);
    await openApp(page, app.url);
    await expect(title(page)).toHaveText(off);
    await shot('off');
    v = copyViews.copying;
    await expect(page.locator('#c-flight li').first()).toHaveText(longPath + '40%');
    await shot('copying');
    v = copyViews.failures;
    await expect(page.locator('#c-failed')).toBeVisible();
    await page.getByText('Show files').click();
    await shot('failures');
    v = { state: 'inuse', on: false, chip: 'Copying is off', title: inUse, text: "It's in another seestar-copy window or seestar-copy-cli. Stop that copy, then start copying here." };
    await expect(title(page)).toHaveText(inUse);
    await shot('inuse');
  });
}
