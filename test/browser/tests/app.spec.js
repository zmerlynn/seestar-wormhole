// The seestar-copy app page: Setup, Settings, field errors and the Browse
// tab, against the browser-only app and the Samba fixture's browse tree.
// The Copy tab's tests are in copy.spec.js.
const path = require('node:path');
const { test, expect, share, refused, readSettings, frame, browseStatus, tab, field, openApp, fakeStatus, copyViews, freeViews } = require('./app');

const rootStatus = '2 folders, 2 files.';
const connected = /^Connected\. The telescope's share has the folders (Long names and MyWorks|MyWorks and Long names)\.$/;
const refusedMessage = "127.0.0.1 answered but refused port 1. Is this the telescope's address?";

// setUp is a saved Setup for the fixture that doesn't start copying, with
// a key from a later version.
const setUp = (folder) => ({ version: 1, address: share, folder, start_copying: false, later: { kept: true } });

test('Setup saves the address and folder and opens the Copy tab', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await expect(page.getByRole('heading', { name: 'Welcome to seestar-copy' })).toBeVisible();
  await expect(page.getByRole('tablist')).toBeHidden();
  await expect(field(page, 'Telescope address')).toBeFocused();
  await expect(field(page, 'Folder for your images')).toHaveValue(path.join(app.home, 'Seestar'));
  // Setup has only the two fields.
  await expect(field(page, 'Leave out')).toBeHidden();
  await expect(page.getByText('Start copying when the app opens')).toBeHidden();

  await field(page, 'Telescope address').fill(share);
  await field(page, 'Folder for your images').fill(app.folder);
  await page.getByRole('button', { name: 'Save without copying' }).click();

  // Save went away: focus is on the new state's heading, with Copy selected.
  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#c-title')).toBeFocused();
  await expect(page.locator('#c-title')).toHaveText('Copying is off.');
  await expect(page.locator('#haddr')).toHaveText(share);
  // Test ran on Save; the header shows the contact, and focus stays.
  await expect(page.locator('#hdot')).toHaveText('Reachable');
  await expect(page.locator('#c-title')).toBeFocused();

  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  // Browse's own header is hidden; its statuses are on, all new.
  await expect(frame(page).locator('header')).toBeHidden();
  await expect(frame(page).locator('#list.statuses')).toBeVisible();
  await expect(frame(page).locator('.badge.new').first()).toBeVisible();

  const saved = readSettings(app.file);
  expect(saved).toMatchObject({ version: 1, address: share, folder: app.folder, interval_minutes: 5, skip_built_in: true, start_copying: true });
});

// countTests counts the page's Test requests.
function countTests(page) {
  const n = { tests: 0 };
  page.on('request', (r) => {
    if (r.url().endsWith('/app/api/test')) n.tests++;
  });
  return n;
}

test('a Test passed before Setup is saved shows in the header without another Test', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const n = countTests(page);
  await field(page, 'Telescope address').fill(share);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(page.locator('#f-test')).toHaveText(connected);
  await field(page, 'Folder for your images').fill(app.folder);
  await page.getByRole('button', { name: 'Save without copying' }).click();
  await expect(page.locator('#hdot')).toHaveText('Reachable');
  await expect(page.locator('#hdot')).toBeVisible();
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  expect(n.tests).toBe(1);
});

test('Setup field errors block saving and focus the first field', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await field(page, 'Telescope address').fill('http://192.0.2.10/');
  await field(page, 'Folder for your images').fill('Seestar');
  await page.getByRole('button', { name: 'Save without copying' }).click();

  const addr = field(page, 'Telescope address');
  const folder = field(page, 'Folder for your images');
  await expect(page.locator('#f-addr-err')).toHaveText('Enter the telescope address, with an optional port.');
  await expect(page.locator('#f-folder-err')).toHaveText('Enter the full path of a folder.');
  await expect(addr).toHaveAttribute('aria-invalid', 'true');
  await expect(folder).toHaveAttribute('aria-invalid', 'true');
  await expect(addr).toBeFocused();
  // The typed values stay.
  await expect(folder).toHaveValue('Seestar');

  await addr.fill(share);
  await page.getByRole('button', { name: 'Save without copying' }).click();
  await expect(page.locator('#f-addr-err')).toBeHidden();
  await expect(addr).not.toHaveAttribute('aria-invalid', 'true');
  await expect(folder).toBeFocused();
  await expect(page.getByRole('heading', { name: 'Welcome to seestar-copy' })).toBeVisible();
  expect(() => readSettings(app.file)).toThrow();
});

test('Test reports success and failure under the address', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const result = page.locator('#f-test');

  await field(page, 'Telescope address').fill(share);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(result).toHaveText(connected);
  await expect(result).toHaveClass(/\bok\b/);
  await expect(result).toHaveAttribute('role', 'status');

  await field(page, 'Telescope address').fill(refused);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(result).toHaveText(refusedMessage);
  await expect(result).toHaveClass(/\berr\b/);

  await field(page, 'Telescope address').fill('a b');
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(page.locator('#f-addr-err')).toHaveText('Enter the telescope address, with an optional port.');
  await expect(result).toHaveText('');
  await expect(field(page, 'Telescope address')).toBeFocused();
  expect(() => readSettings(app.file)).toThrow();
});

test('the tabs follow the tab pattern', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openApp(page, app.url);
  const copy = tab(page, 'Copy');
  const browse = tab(page, 'Browse');
  const free = tab(page, 'Free up space');
  const settings = tab(page, 'Settings');
  await expect(copy).toHaveAttribute('aria-selected', 'true');
  await expect(copy).toHaveAttribute('tabindex', '0');
  await expect(browse).toHaveAttribute('tabindex', '-1');
  await expect(free).toHaveAttribute('tabindex', '-1');
  await expect(settings).toHaveAttribute('tabindex', '-1');
  await expect(page.locator('#p-browse')).toBeHidden();
  await expect(page.locator('#p-free')).toBeHidden();
  await expect(page.locator('#p-settings')).toBeHidden();

  await copy.focus();
  await page.keyboard.press('ArrowRight');
  await expect(browse).toBeFocused();
  await expect(browse).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tabpanel', { name: 'Browse' })).toBeVisible();
  await expect(page.locator('#p-copy')).toBeHidden();
  await page.keyboard.press('ArrowRight');
  await expect(free).toBeFocused();
  await expect(page.getByRole('tabpanel', { name: 'Free up space' })).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(settings).toBeFocused();
  await expect(page.getByRole('tabpanel', { name: 'Settings' })).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(copy).toBeFocused();
  await page.keyboard.press('End');
  await expect(settings).toBeFocused();
  await page.keyboard.press('Home');
  await expect(copy).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(settings).toBeFocused();
  await expect(settings).toHaveAttribute('aria-selected', 'true');

  await browse.click();
  await expect(page.getByRole('tabpanel', { name: 'Browse' })).toBeVisible();
});

test('Settings saves every field and keeps unknown keys', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openApp(page, app.url);
  await tab(page, 'Settings').click();
  await expect(field(page, 'Telescope address')).toHaveValue(share);
  await expect(page.locator('#f-builtin')).toHaveText(/^\.\* · \*\.tmp · .* · \/batch_stack_tmp$/);
  await expect(page.locator('#version')).toHaveText(/^seestar-copy \S+$/);

  await field(page, 'Folder for your images').fill(app.folder);
  await page.locator('#f-int').fill('30');
  await field(page, 'Leave out').fill('Video\n\n/Preview');
  await field(page, 'Only copy').fill('MyWorks');
  await page.getByLabel(/Skip the telescope's logs/).uncheck();
  await page.getByLabel('Start copying when the app opens').uncheck();
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');

  expect(readSettings(app.file)).toEqual({
    version: 1, address: share, folder: app.folder, interval_minutes: 30,
    excludes: ['Video', '/Preview'], includes: ['MyWorks'], skip_built_in: false, start_copying: false,
    older_than_days: 7, trust_state: false,
    later: { kept: true },
  });
  // A reload shows what was saved.
  await page.reload();
  await expect(page.locator('#loading')).toBeHidden();
  await tab(page, 'Settings').click();
  await expect(field(page, 'Leave out')).toHaveValue('Video\n/Preview');
  await expect(page.locator('#f-int')).toHaveValue('30');
  await expect(page.getByLabel('Start copying when the app opens')).not.toBeChecked();
});

test('Settings field errors show under each field and nothing is saved', async ({ page, startApp }) => {
  const before = setUp('/nowhere');
  const app = await startApp(before);
  await openApp(page, app.url);
  await tab(page, 'Settings').click();
  await page.locator('#f-int').fill('0');
  await field(page, 'Leave out').fill('Video\n\n[Pp]review');
  await field(page, 'Only copy').fill('MyWorks/');
  await page.getByRole('button', { name: 'Save', exact: true }).click();

  await expect(page.locator('#f-int-err')).toHaveText('Enter a whole number of minutes from 1 to 1440.');
  await expect(page.locator('#f-ex-err')).toHaveText('Line 3: [ and \\ are not supported; use * and ?, and / between folders. Correct this pattern and save again.');
  await expect(page.locator('#f-in-err')).toHaveText('Line 1: omit the trailing /; a pattern matches files and folders. Correct this pattern and save again.');
  await expect(page.locator('#f-int')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#f-int')).toBeFocused();
  await expect(page.locator('#f-saved')).toHaveText('');
  expect(readSettings(app.file)).toEqual(before);

  await page.locator('#f-int').fill('10');
  await field(page, 'Leave out').fill('Video\n\n*review');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(field(page, 'Only copy')).toBeFocused();
  await expect(page.locator('#f-int-err')).toBeHidden();
  await expect(page.locator('#f-ex-err')).toBeHidden();
  await expect(page.locator('#f-int')).not.toHaveAttribute('aria-invalid', 'true');
});

test('a new address replaces Browse, and its errors say to select Share', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openApp(page, app.url);
  const n = countTests(page);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  await frame(page).getByRole('link', { name: 'MyWorks/', exact: true }).click();
  await expect(browseStatus(page)).toHaveText('1 folder, 0 files.');

  await tab(page, 'Settings').click();
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(page.locator('#f-test')).toHaveText(connected);
  await expect(page.locator('#hdot')).toHaveText('Reachable');
  await field(page, 'Telescope address').fill(refused);
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(page.locator('#haddr')).toHaveText(refused);
  // Test ran on Save, for the saved address.
  await expect(page.locator('#f-test')).toHaveText(refusedMessage);
  await expect(page.locator('#hdot')).toHaveText("Can't reach");

  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(/\. Select Share or a parent folder above to retry\.$/);
  await expect(browseStatus(page)).not.toContainText('reload');

  await tab(page, 'Settings').click();
  // Back to the address that passed: its contact shows, with no new Test.
  await field(page, 'Telescope address').fill(share);
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('#hdot')).toHaveText('Reachable');
  await expect(page.locator('#hdot')).toBeVisible();
  await tab(page, 'Browse').click();
  // The frame is back at the share's top.
  await expect(browseStatus(page)).toHaveText(rootStatus);
  expect(n.tests).toBe(2);
});

test('Save is off until its answer is in', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openApp(page, app.url);
  await tab(page, 'Settings').click();
  let posts = 0;
  let release;
  const held = new Promise((resolve) => (release = resolve));
  await page.route('**/app/api/settings', async (route) => {
    posts++;
    await held;
    await route.continue();
  });
  const save = page.getByRole('button', { name: 'Save', exact: true });
  await save.click();
  await expect(save).toBeDisabled();
  // Enter in a field would submit again.
  await field(page, 'Folder for your images').press('Enter');
  release();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(save).toBeEnabled();
  expect(posts).toBe(1);
});

test('the app page refuses a request without its token', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  const base = app.url.split('#')[0];
  await page.goto(base);
  const codes = await page.evaluate(async () => {
    const status = await fetch('/app/api/status');
    const save = await fetch('/app/api/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    return [status.status, save.status];
  });
  expect(codes).toEqual([403, 403]);
  await expect(page.locator('#loading')).toHaveText("Can't load seestar-copy. Close this window and reopen seestar-copy.");
});

for (const colorScheme of ['light', 'dark']) {
  test(`app screenshots in ${colorScheme}`, async ({ page, startApp }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    const shot = (name) => page.screenshot({
      path: path.join(__dirname, '..', 'screenshots', `${testInfo.project.name}-${colorScheme}-app-${name}.png`),
    });
    const setup = await startApp();
    await openApp(page, setup.url);
    await field(page, 'Telescope address').fill('a b');
    await page.getByRole('button', { name: 'Test' }).click();
    await expect(page.locator('#f-addr-err')).toBeVisible();
    await shot('setup-error');

    const app = await startApp(setUp('/nowhere'));
    await openApp(page, app.url);
    await expect(page.locator('#c-title')).toHaveText('Copying is off.');
    await shot('copy-off');
    await tab(page, 'Browse').click();
    await expect(browseStatus(page)).toHaveText(rootStatus);
    await shot('browse');
    await tab(page, 'Settings').click();
    await field(page, 'Leave out').fill('Video\n[Pp]review');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.locator('#f-ex-err')).toBeVisible();
    await page.getByRole('button', { name: 'Test' }).click();
    await expect(page.locator('#f-test')).toHaveText(connected);
    await shot('settings');
  });
}

// The layouts DESIGN asks the app to survive: twice the text size, the
// wider text spacing of WCAG 1.4.12, and a 480 px wide window.
const layouts = {
  'text-200': { width: 1280 },
  'text-spacing': { width: 1280 },
  'narrow-480': { width: 480 },
};

// restyle applies a layout's text changes to one document, through the
// CSSOM, which the pages' style policy allows.
async function restyle(target, layout) {
  await target.evaluate((layout) => {
    const all = [document.documentElement, ...document.querySelectorAll('*')];
    if (layout === 'text-200') {
      const sizes = all.map((el) => parseFloat(getComputedStyle(el).fontSize));
      all.forEach((el, i) => el.style.setProperty('font-size', sizes[i] * 2 + 'px', 'important'));
    }
    if (layout === 'text-spacing') {
      for (const el of all) {
        el.style.setProperty('line-height', '1.5', 'important');
        el.style.setProperty('letter-spacing', '0.12em', 'important');
        el.style.setProperty('word-spacing', '0.16em', 'important');
      }
      for (const p of document.querySelectorAll('p')) p.style.setProperty('margin-bottom', '2em', 'important');
    }
  }, layout);
}

// fits checks that the page does not scroll sideways and that every
// visible control is inside the window once scrolled to.
async function fits(page) {
  const width = page.viewportSize().width;
  const [scrollWidth, clientWidth] = await page.evaluate(
    () => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  for (const control of await page.locator('button, input, textarea, iframe').filter({ visible: true }).all()) {
    await control.scrollIntoViewIfNeeded();
    const box = await control.boundingBox();
    const name = await control.evaluate((el) => el.id || el.textContent);
    expect(box.x, name).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width, name).toBeLessThanOrEqual(width + 0.5);
  }
}

// tabsWrap checks that the tabs wrap rather than overflow.
async function tabsWrap(page) {
  const [wrap, overflows] = await page.locator('nav.tabs').evaluate(
    (nav) => [getComputedStyle(nav).flexWrap, nav.scrollWidth > nav.clientWidth]);
  expect(wrap).toBe('wrap');
  expect(overflows).toBe(false);
}

for (const [layout, { width }] of Object.entries(layouts)) {
  test(`the app fits with ${layout}`, async ({ page, startApp }, testInfo) => {
    await page.setViewportSize({ width, height: 800 });
    const shot = (name) => page.screenshot({
      fullPage: true,
      path: path.join(__dirname, '..', 'screenshots', `${testInfo.project.name}-app-${layout}-${name}.png`),
    });

    const setup = await startApp();
    await openApp(page, setup.url);
    await restyle(page, layout);
    await fits(page);
    await shot('setup');

    const app = await startApp(setUp('/nowhere'));
    await openApp(page, app.url);
    await expect(page.locator('#c-title')).toHaveText('Copying is off.');
    await restyle(page, layout);
    await fits(page);
    await tabsWrap(page);
    await shot('copy');

    await tab(page, 'Browse').click();
    await expect(browseStatus(page)).toHaveText(rootStatus);
    const browse = await (await page.locator('#b-frame').elementHandle()).contentFrame();
    await restyle(page, layout);
    await restyle(browse, layout);
    await fits(page);
    await tabsWrap(page);
    // Browse's table scrolls in its own box; its page does not widen.
    const [scrollWidth, clientWidth, overflowX] = await browse.evaluate(() => [
      document.documentElement.scrollWidth, document.documentElement.clientWidth,
      getComputedStyle(document.querySelector('.scroll')).overflowX]);
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
    expect(overflowX).toBe('auto');
    await shot('browse');

    await tab(page, 'Free up space').click();
    await fits(page);
    await tabsWrap(page);
    await shot('free');

    await tab(page, 'Settings').click();
    await fits(page);
    await tabsWrap(page);
    await shot('settings');

    // An active pass, with long paths in flight and failed, and the lists
    // open.
    let v = null;
    await fakeStatus(page, () => v);
    for (const [name, view] of Object.entries(copyViews)) {
      v = view;
      await page.reload();
      await expect(page.locator('#loading')).toBeHidden();
      await expect(page.locator('#c-title')).toHaveText(view.title);
      if (view.failed) await page.getByText('Show files').click();
      if (view.activity) await page.getByText('Recent activity').click();
      await restyle(page, layout);
      await fits(page);
      await shot('copy-' + name);
    }

    // Free up space's states, with a long path, its lists open and the
    // confirm dialog.
    v = null;
    let f = null;
    await fakeStatus(page, () => v, () => f);
    for (const [name, view] of Object.entries(freeViews)) {
      f = view;
      await page.reload();
      await expect(page.locator('#loading')).toBeHidden();
      await tab(page, 'Free up space').click();
      await expect(page.locator('#x-title')).toHaveText(view.title);
      for (const s of await page.locator('#p-free summary').filter({ visible: true }).all()) await s.click();
      await restyle(page, layout);
      await fits(page);
      await shot('free-' + name);
      if (name === 'preview') {
        await page.locator('#x-go').click();
        await expect(page.getByRole('dialog')).toBeVisible();
        await fits(page);
        await shot('free-confirm');
        await page.keyboard.press('Escape');
      }
    }
  });
}
