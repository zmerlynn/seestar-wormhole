// The app's page: the first run on Connection and Copy, the address on
// this network, Copy settings and their field errors, the tabs and the
// Browse tab, against the page served without a window and the Samba
// fixture's browse tree.
// The Copy tab's passes are in copy.spec.js.
const path = require('node:path');
const fs = require('node:fs');
const {
  test, expect, share, refused, lan, readJSON, frame, browseStatus, tab, field, openApp, settleStartFocus, openCopy, fakeStatus, copyViews, freeViews,
} = require('./app');

const rootStatus = '2 folders, 2 files.';
const shares = String.raw`Connected\. The telescope's share has the folders (Long names and MyWorks|MyWorks and Long names)\.`;
// A passed Test of the saved address, and of any other, which says to Save.
const connected = new RegExp(`^${shares}$`);
const connectedUnsaved = new RegExp(`^${shares} Save to use this address for Copy, Browse and Free up space\\.$`);
const refusedMessage = "127.0.0.1 answered but refused port 1. Is this the telescope's address?";
const addressError = 'Enter the telescope address, with an optional port.';

// setUp is a saved route to the fixture and copy settings that don't
// start copying, with a key from a later version.
const setUp = (folder) => ({
  'config.json': lan(), 'copy.json': { version: 1, folder, start_copying: false, later: { kept: true } },
});

// The fixture's share as the header names it, a telescope saved by its
// address.
const shareName = 'Seestar 127.0.0.1 14450';

const lanResult = (page) => page.locator('#lan-result');
const copySave = (page) => page.locator('#f-save');

// chooseLAN picks the route on this network on Connection.
async function chooseLAN(page) {
  await page.getByRole('radio', { name: 'On this network', exact: true }).check();
  await expect(field(page, 'Telescope address')).toBeVisible();
}

test('the first run opens on Connection; saving an address opens Copy, whose first Save shows copying off', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const connection = tab(page, 'Connection');
  await expect(connection).toHaveAttribute('aria-selected', 'true');
  for (const name of ['Copy', 'Browse', 'Free up space']) await expect(tab(page, name)).toBeDisabled();
  await expect(page.locator('#tabs-note')).toBeVisible();
  await expect(page.locator('#connection-text')).toHaveText('Not connected. No telescope chosen yet.');
  await expect(page.locator('#telescope-to')).toBeHidden();
  await expect(page.getByLabel('Remotely through Telescope Network')).toBeChecked();
  // The arrow keys pass over the closed tabs.
  await settleStartFocus(page);
  await connection.focus();
  await page.keyboard.press('ArrowRight');
  await expect(tab(page, 'Activity')).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(connection).toBeFocused();
  await expect(connection).toHaveAttribute('aria-selected', 'true');

  await chooseLAN(page);
  await field(page, 'Telescope address').fill(share);
  await page.locator('#lan-save').click();
  await expect(lanResult(page)).toHaveText(`Saved. ${share} is reachable.`);
  await expect(lanResult(page)).toHaveAttribute('data-tone', 'ok');
  // Save ran Test; the header shows it.
  await expect(page.locator('#connection-text')).toHaveText(`${shareName} on this network, reachable`);
  await expect(page.locator('#connection-dot')).toHaveAttribute('data-tone', 'on');
  await expect(page.locator('#tabs-note')).toBeHidden();
  expect(readJSON(app.config)).toMatchObject({ route: 'lan', lan_address: share, route_established: true });
  expect(fs.existsSync(app.copyFile)).toBe(false);

  // Copy's first visit has its settings open, with the default folder and
  // no example values.
  await tab(page, 'Copy').click();
  await expect(page.locator('#s-first')).toHaveText('Choose the folder for your images, then press Save. Copying starts later, when you press Start copying.');
  await expect(page.locator('#c-main')).toBeHidden();
  await expect(field(page, 'Folder for your images')).toHaveValue(path.join(app.home, 'Seestar'));
  await expect(field(page, 'Leave out')).toBeVisible();
  await expect(page.getByLabel('Start copying when the app opens')).not.toBeChecked();
  // The one placeholder is the history folder's default, not an example.
  for (const box of await page.locator('#copy-form input:not(#f-hist), #copy-form textarea').all()) {
    expect(await box.getAttribute('placeholder')).toBeNull();
  }
  await expect(field(page, 'Copy history folder')).toHaveAttribute('placeholder', /[/\\]seestar-wormhole[/\\]copy-history$/);
  await expect(page.locator('#f-ex-help')).toContainText('For example: Video, /Preview.');
  await expect(page.locator('#f-cancel')).toBeHidden();
  // Browse shows the telescope's files before the folder is saved,
  // without statuses.
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  await expect(frame(page).getByRole('link', { name: 'MyWorks/' })).toBeVisible();
  await expect(frame(page).locator('#list')).not.toHaveClass(/statuses/);
  await expect(frame(page).locator('#list .badge')).toHaveCount(0);
  await expect(frame(page).locator('#key')).toBeHidden();
  await tab(page, 'Copy').click();

  await field(page, 'Folder for your images').fill(app.folder);
  await expect(copySave(page)).toHaveText('Save');
  await copySave(page).click();

  // Copying is off, and the notice that says Start copying is next has
  // focus, just above it.
  const next = page.locator('#c-next');
  await expect(next).toBeFocused();
  await expect(next).toHaveText('Settings saved. Copying starts when you press Start copying. Preview shows what it would copy first.');
  await expect(page.locator('#c-title')).toHaveText('Copying is off.');
  await expect(page.getByRole('button', { name: 'Start copying' })).toBeVisible();
  await expect(page.locator('#s-first')).toBeHidden();
  await expect(page.locator('#copy-chip')).toBeHidden();

  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  // Browse's own header is hidden; its statuses are on, all new.
  await expect(frame(page).locator('header')).toBeHidden();
  await expect(frame(page).locator('#list.statuses')).toBeVisible();
  await expect(frame(page).locator('.badge.new').first()).toBeVisible();

  expect(readJSON(app.copyFile)).toMatchObject({
    version: 1, folder: app.folder, interval_minutes: 5, files_at_once: 4, skip_built_in: true, start_copying: false, once: false,
  });
});

// Saving the address makes it the copy tabs' telescope at once: Copy's
// first Save straight after it isn't refused as for the telescope before.
test("Copy's first Save straight after saving the address is the new telescope's", async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await chooseLAN(page);
  await field(page, 'Telescope address').fill(share);
  await page.locator('#lan-save').click();
  await expect(lanResult(page)).toHaveText(`Saved. ${share} is reachable.`);
  await tab(page, 'Copy').click();
  await copySave(page).click();
  await expect(page.locator('#c-next')).toBeFocused();
  expect(readJSON(app.copyFile)).toMatchObject({ folder: app.folder });
});

test('a failed first Save is off while it runs and says so, and Enter saves', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': lan() });
  await openCopy(page, app.url);
  // A folder from the window's picker fills the field.
  await page.route('**/copy/api/folder', (route) => route.fulfill({ json: { folder: app.folder } }));
  await expect(page.locator('#f-choose')).toBeHidden();
  await page.locator('#f-choose').dispatchEvent('click');
  await expect(field(page, 'Folder for your images')).toHaveValue(app.folder);

  let release;
  const held = new Promise((resolve) => (release = resolve));
  await page.route('**/copy/api/settings', async (route) => {
    await held;
    await route.fulfill({ status: 500, body: 'no' });
  });
  try {
    await copySave(page).click();
    await expect(copySave(page)).toBeDisabled();
  } finally {
    release();
  }
  await expect(page.locator('#f-saved')).toHaveText("Couldn't save settings. Your changes have not been saved. Try saving again.");
  await expect(page.locator('#f-saved')).toBeFocused();
  await expect(page.locator('#f-saved')).toBeInViewport();
  await expect(copySave(page)).toBeEnabled();
  expect(fs.existsSync(app.copyFile)).toBe(false);
  await page.unroute('**/copy/api/settings');

  // Enter in a field saves, and opens Copy with copying off; focus is on
  // the notice that says Start copying is next, not on Start copying.
  await field(page, 'Folder for your images').press('Enter');
  await expect(page.locator('#c-next')).toBeFocused();
  await expect(page.locator('#c-title')).toHaveText('Copying is off.');
  expect(readJSON(app.copyFile)).toMatchObject({ folder: app.folder, start_copying: false });
});

// Connection says what each way of reaching the telescope gives, and
// what the address on this network is for.
test('Connection says what each route and the address are for', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const purpose = "Through Telescope Network, one connection serves both the Seestar app and the telescope's files. On this network, it serves only the telescope's files, for Copy, Browse and Free up space.";
  await expect(page.locator('#route-purpose')).toHaveText(purpose);
  await expect(page.getByRole('group', { name: 'Reach the telescope' })).toHaveAccessibleDescription(new RegExp('^' + purpose.replace(/\./g, '\\$&')));
  await expect(page.locator('#route-purpose')).toBeVisible();
  await chooseLAN(page);
  await expect(page.locator('#route-purpose')).toBeVisible();
  await expect(field(page, 'Telescope address')).toHaveAccessibleDescription(
    "Pick a telescope above, or type its address on this network, with an optional port. Port 445 is the default. Copy, Browse and Free up space read the telescope's files at this address.");
});

// countTests counts the page's Test requests.
function countTests(page) {
  const n = { tests: 0 };
  page.on('request', (r) => {
    if (r.url().endsWith('/copy/api/test')) n.tests++;
  });
  return n;
}

test('a Test that passed is not run again by Save, and the header shows it', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const n = countTests(page);
  await chooseLAN(page);
  await field(page, 'Telescope address').fill(share);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(lanResult(page)).toHaveText(connectedUnsaved);
  await expect(page.locator('#connection-text')).toHaveText('Not connected. No telescope chosen yet.');
  // Test saves nothing: the tabs stay locked until Save.
  await expect(tab(page, 'Copy')).toBeDisabled();
  // While Save checks the address, the result line says so.
  let release;
  const held = new Promise((resolve) => (release = resolve));
  await page.route('**/api/route', async (route) => {
    await held;
    await route.continue();
  });
  try {
    await page.locator('#lan-save').click();
    await expect(lanResult(page)).toHaveText(`Checking ${share}…`);
    await expect(page.locator('#lan-save')).toHaveText('Saving…');
  } finally {
    release();
  }
  await expect(lanResult(page)).toHaveText(`Saved. ${share} is reachable.`);
  await expect(page.locator('#connection-text')).toHaveText(`${shareName} on this network, reachable`);
  for (const name of ['Copy', 'Browse', 'Free up space']) await expect(tab(page, name)).toBeEnabled();
  expect(n.tests).toBe(1);
});

// The suffix and the header compare endpoints, not their spelling: the
// saved address in another case, IPv6 form or port spelling is the saved
// address. An address kept while Telescope Network is the route is not.
test('Test of the saved address in another spelling does not say to Save', async ({ page, startApp }) => {
  const cases = [
    ['scope.local:445', 'SCOPE.Local:0445'],
    ['[2001:db8::1]:445', '[2001:DB8:0:0:0:0:0:1]:445'],
    ['[::ffff:192.0.2.7]:445', '192.0.2.7:445'],
    ['[fe80::1%ethA]:445', '[FE80:0:0:0:0:0:0:1%ethA]:445'],
  ];
  const message = "Connected. The telescope's share has the folder MyWorks.";
  for (const [saved, answered] of cases) {
    const app = await startApp({ 'config.json': lan(saved) });
    await page.route('**/copy/api/test', (r) => r.fulfill({ json: { ok: true, address: answered, message } }));
    await openApp(page, app.url);
    await field(page, 'Telescope address').fill(answered);
    await page.getByRole('button', { name: 'Test' }).click();
    await expect(lanResult(page)).toHaveText(message);
    await expect(page.locator('#connection-text')).toHaveText(/^Seestar .+ on this network, reachable$/);
    await page.unroute('**/copy/api/test');
  }
});

// An IPv6 zone names an interface, and interface names differ by case.
test('Test of the saved address on another interface says to Save', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': lan('[fe80::1%ethA]:445') });
  await page.route('**/copy/api/test', (r) => r.fulfill({ json: { ok: true, address: '[fe80::1%etha]:445', message: 'Connected.' } }));
  await openApp(page, app.url);
  await field(page, 'Telescope address').fill('[fe80::1%etha]:445');
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(lanResult(page)).toHaveText('Connected. Save to use this address for Copy, Browse and Free up space.');
});

test('Test of an address kept while Telescope Network is the route says to Save', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), route: 'account' } });
  await openApp(page, app.url);
  await chooseLAN(page);
  await expect(field(page, 'Telescope address')).toHaveValue(share);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(lanResult(page)).toHaveText(connectedUnsaved);
});

test('Test reports success and failure under the address, and Save keeps a refused address out', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await chooseLAN(page);
  const result = lanResult(page);

  await field(page, 'Telescope address').fill(share);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(result).toHaveText(connectedUnsaved);
  await expect(result).toHaveAttribute('data-tone', 'ok');
  await expect(result).toHaveAttribute('role', 'status');

  await field(page, 'Telescope address').fill(refused);
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(result).toHaveText(refusedMessage);
  await expect(result).toHaveAttribute('data-tone', 'error');

  // Save tests a new address first, and saves nothing it can't reach.
  await page.locator('#lan-save').click();
  await expect(result).toHaveText(refusedMessage);
  await expect(tab(page, 'Copy')).toBeDisabled();
  expect(fs.existsSync(app.config) ? readJSON(app.config).route_established : undefined).toBeFalsy();

  await field(page, 'Telescope address').fill('a b');
  await page.getByRole('button', { name: 'Test' }).click();
  await expect(page.locator('#lan-address-error')).toHaveText(addressError);
  await expect(field(page, 'Telescope address')).toHaveAttribute('aria-invalid', 'true');
  await expect(result).toHaveText('');
  await expect(field(page, 'Telescope address')).toBeFocused();
  // Typing clears the error.
  await field(page, 'Telescope address').press('End');
  await field(page, 'Telescope address').type('c');
  await expect(page.locator('#lan-address-error')).toBeHidden();
});

// The status poll renders every second. A press held across a render
// still clicks: WebKit drops a click whose press and release straddle a
// rewrite of the button's text.
test('a click held across a status poll still clicks', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await chooseLAN(page);
  await field(page, 'Telescope address').fill(share);
  // The scan's list and Scan button above put Test below the fold.
  await page.locator('#lan-test').scrollIntoViewIfNeeded();
  const box = await page.locator('#lan-test').boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  // Copy's status is read once the page has rendered its own.
  await page.waitForResponse((r) => r.url().endsWith('/copy/api/status'));
  await page.mouse.up();
  await expect(lanResult(page)).toHaveText(connectedUnsaved);
});

// The scan's list is a row per telescope, its name, model, status and
// addresses in columns that line up across rows; narrow, a row wraps but
// stays apart from the next.
test('the scan lists a row per telescope in aligned columns', async ({ page, startApp }) => {
  const app = await startApp();
  const telescopes = [
    { name: 'S50_Garden', model: 'Seestar S50', addresses: ['192.0.2.20'], busy: false, saved: true },
    { name: 'S30_Roof', model: 'Seestar S30', addresses: ['192.0.2.40', '192.0.2.41'], busy: true, saved: false },
    { name: 'S50_Pro_Shed', model: 'Seestar S50 Pro', addresses: ['192.0.2.60'], busy: false, saved: false },
  ];
  await page.route('**/api/scan', (route) => route.fulfill({ json: { ok: true, telescopes, replies: 4, blocked: false, message: '' } }));
  await openApp(page, app.url);
  await chooseLAN(page);
  const list = page.getByRole('list', { name: 'Telescopes on this network' });
  const rows = list.getByRole('listitem');
  await expect(rows).toHaveCount(3);
  await expect(page.locator('#lan-scan-result')).toHaveText('Found 3 telescopes. Pick an address to fill it in. A busy telescope may refuse the phone app; copying still works.');
  const cells = (i) => rows.nth(i).locator(':scope > span:not(.lan-addresses)').allTextContents();
  expect(await cells(0)).toEqual(['S50_Garden', 'Seestar S50', 'saved']);
  expect(await cells(1)).toEqual(['S30_Roof', 'Seestar S30', 'busy']);
  expect(await cells(2)).toEqual(['S50_Pro_Shed', 'Seestar S50 Pro', '']);
  for (const [name, address] of [['S50_Garden', '192.0.2.20'], ['S30_Roof', '192.0.2.40'], ['S30_Roof', '192.0.2.41'], ['S50_Pro_Shed', '192.0.2.60']]) {
    await expect(list.getByRole('button', { name: `Use ${address} for ${name}`, exact: true })).toHaveText(address);
  }
  // boxes is each row's box and its cells' boxes.
  const boxes = () => rows.evaluateAll((lis) => lis.map((li) => ({
    row: li.getBoundingClientRect().toJSON(),
    cells: [...li.children].map((c) => c.getBoundingClientRect().toJSON()),
  })));
  const wide = await boxes();
  for (const { row, cells } of wide) {
    // One line: the cells side by side, left to right, inside the row.
    for (let i = 1; i < cells.length; i++) expect(cells[i].left).toBeGreaterThanOrEqual(cells[i - 1].right);
    for (const c of cells) expect(c.top >= row.top && c.bottom <= row.bottom).toBe(true);
    // Aligned: each column starts where the first row's does.
    expect(cells.map((c) => Math.round(c.left))).toEqual(wide[0].cells.map((c) => Math.round(c.left)));
  }
  for (let i = 1; i < wide.length; i++) expect(wide[i].row.top).toBeGreaterThanOrEqual(wide[i - 1].row.bottom);

  // Narrow, each row wraps onto two lines, still apart, with no sideways
  // scroll.
  await page.setViewportSize({ width: 360, height: 800 });
  await expect(async () => {
    const narrow = await boxes();
    for (const { row, cells } of narrow) {
      expect(cells[2].top).toBeGreaterThanOrEqual(cells[0].bottom);
      for (const c of cells) expect(c.top >= row.top && c.bottom <= row.bottom && c.right <= row.right + 1).toBe(true);
    }
    for (let i = 1; i < narrow.length; i++) expect(narrow[i].row.top).toBeGreaterThanOrEqual(narrow[i - 1].row.bottom);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }).toPass();

  // A pick fills the address in.
  await list.getByRole('button', { name: 'Use 192.0.2.41 for S30_Roof', exact: true }).click();
  await expect(field(page, 'Telescope address')).toHaveValue('192.0.2.41');
  await expect(lanResult(page)).toHaveText('Picked S30_Roof at 192.0.2.41. Test or Save to use it.');
});

test('first-run field errors block saving and focus the first field', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': lan() });
  await openCopy(page, app.url);
  const folder = field(page, 'Folder for your images');
  await folder.fill('Seestar');
  await page.locator('#f-int').fill('0');
  await copySave(page).click();

  await expect(page.locator('#f-folder-err')).toHaveText('Enter the full path of a folder.');
  await expect(page.locator('#f-int-err')).toHaveText('Enter a whole number of minutes from 1 to 1440.');
  await expect(folder).toHaveAttribute('aria-invalid', 'true');
  await expect(folder).toBeFocused();
  // The typed values stay.
  await expect(folder).toHaveValue('Seestar');

  await folder.fill(app.folder);
  await copySave(page).click();
  await expect(page.locator('#f-folder-err')).toBeHidden();
  await expect(folder).not.toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#f-int')).toBeFocused();
  await expect(page.locator('#s-first')).toBeVisible();
  expect(fs.existsSync(app.copyFile)).toBe(false);
});

test('the tabs follow the tab pattern', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openApp(page, app.url);
  const connection = tab(page, 'Connection');
  const copy = tab(page, 'Copy');
  const browse = tab(page, 'Browse');
  const free = tab(page, 'Free up space');
  const activity = tab(page, 'Activity');
  await expect(connection).toHaveAttribute('aria-selected', 'true');
  await expect(connection).toHaveAttribute('tabindex', '0');
  for (const t of [copy, browse, free, activity]) await expect(t).toHaveAttribute('tabindex', '-1');
  for (const p of ['copy', 'browse', 'free', 'activity']) await expect(page.locator('#panel-' + p)).toBeHidden();

  await connection.focus();
  await page.keyboard.press('ArrowRight');
  await expect(copy).toBeFocused();
  await expect(copy).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tabpanel', { name: 'Copy' })).toBeVisible();
  await expect(page.locator('#panel-connection')).toBeHidden();
  await page.keyboard.press('ArrowRight');
  await expect(browse).toBeFocused();
  await expect(page.getByRole('tabpanel', { name: 'Browse' })).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(free).toBeFocused();
  await expect(page.getByRole('tabpanel', { name: 'Free up space' })).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(activity).toBeFocused();
  await expect(page.getByRole('tabpanel', { name: 'Activity' })).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(connection).toBeFocused();
  await page.keyboard.press('End');
  await expect(activity).toBeFocused();
  await page.keyboard.press('Home');
  await expect(connection).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(activity).toBeFocused();
  await expect(activity).toHaveAttribute('aria-selected', 'true');

  await browse.click();
  await expect(page.getByRole('tabpanel', { name: 'Browse' })).toBeVisible();
});

// A locked tab is dimmed, refuses the pointer and shows a padlock, and
// its label stays legible, in light and dark.
test('locked tabs look locked', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const look = (name) => tab(page, name).evaluate((e) => {
    const s = getComputedStyle(e), before = getComputedStyle(e, '::before'), after = getComputedStyle(e, '::after');
    let bg = e, color = 'rgba(0, 0, 0, 0)';
    while (bg && /^rgba\(.*, 0\)$|^transparent$/.test(color = getComputedStyle(bg).backgroundColor)) bg = bg.parentElement;
    return {
      cursor: s.cursor, opacity: Number(s.opacity), color: s.color, bg: color,
      lock: before.content !== 'none' && after.content !== 'none' && after.backgroundColor === s.color,
    };
  });
  // shown is color seen at opacity over bg.
  const shown = (color, opacity, bg) => {
    const [c, b] = [color, bg].map((v) => v.match(/\d+/g).slice(0, 3).map(Number));
    return `rgb(${c.map((v, i) => Math.round(v * opacity + b[i] * (1 - opacity))).join(', ')})`;
  };
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    const open = await look('Activity');
    expect(open, colorScheme).toMatchObject({ cursor: 'pointer', opacity: 1, lock: false });
    for (const name of ['Copy', 'Browse', 'Free up space']) {
      await expect(tab(page, name)).toHaveAttribute('aria-describedby', 'tabs-note');
      const locked = await look(name);
      expect(locked, `${name} ${colorScheme}`).toMatchObject({ cursor: 'not-allowed', lock: true });
      expect(locked.opacity, `${name} ${colorScheme}`).toBeLessThanOrEqual(0.75);
      expect(contrast(shown(locked.color, locked.opacity, locked.bg), locked.bg), `${name} ${colorScheme}`).toBeGreaterThanOrEqual(3);
      expect(contrast(shown(locked.color, locked.opacity, locked.bg), locked.bg)).toBeLessThan(contrast(open.color, open.bg));
    }
  }
  await expect(page.locator('#tabs-note')).toBeVisible();
});

// Forced colors drop the padlock body's background; its border, in the
// label's color, still draws it. A browser that can't emulate forced
// colors skips this.
test('the padlock keeps its body under forced colors', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await page.emulateMedia({ forcedColors: 'active' });
  test.skip(!(await page.evaluate(() => matchMedia('(forced-colors: active)').matches)), 'no forced colors emulation');
  for (const name of ['Copy', 'Browse', 'Free up space']) {
    const lock = await tab(page, name).evaluate((e) => {
      const after = getComputedStyle(e, '::after');
      return { label: getComputedStyle(e).color, style: after.borderTopStyle, width: parseFloat(after.borderTopWidth), color: after.borderTopColor };
    });
    expect(lock.style, name).toBe('solid');
    expect(lock.width, name).toBeGreaterThan(0);
    expect(lock.color, name).toBe(lock.label);
  }
});

// Forced colors give the selects their native arrow back: the drawn one
// is dropped (Chromium) or keeps the page's color (WebKit). A browser
// that can't emulate forced colors skips this.
test('the selects are native under forced colors', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const style = (...props) => page.locator('#mode').evaluate((e, props) => props.map((p) => getComputedStyle(e)[p]), props);
  expect(await style('appearance')).toEqual(['none']);
  await page.emulateMedia({ forcedColors: 'active' });
  test.skip(!(await page.evaluate(() => matchMedia('(forced-colors: active)').matches)), 'no forced colors emulation');
  const [appearance, image] = await style('appearance', 'backgroundImage');
  expect(appearance).toBe('auto');
  expect(image).not.toContain('gradient');
});

// The tabs are light: the selected one is underlined, not bold, so no tab
// changes width; a keyboard focus ring sits inside the tab, clear of the
// underline, and a click shows none; and every panel's first heading has
// the same room under the tabs.
test('the selected tab is underlined only, its focus ring clears the underline, and each panel starts as far below the tabs', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await expect(page.locator('#c-title')).toHaveText('Copying is off.');
  const names = ['Connection', 'Copy', 'Browse', 'Free up space', 'Activity'];
  const widths = async () => Promise.all(names.map(async (n) => (await tab(page, n).boundingBox()).width));
  const style = (loc, ...props) => loc.evaluate((e, props) => props.map((p) => getComputedStyle(e)[p]), props);
  // The top of each panel's first text, or of Browse's frame.
  const top = () => page.evaluate(() => {
    const tabs = document.getElementById('tabs').getBoundingClientRect().bottom;
    const panel = [...document.querySelectorAll('[role=tabpanel]')].find((p) => !p.hidden);
    const walk = document.createTreeWalker(panel, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      if (n.nodeName === 'IFRAME' && n.checkVisibility()) return n.getBoundingClientRect().top - tabs;
      if (n.nodeType === Node.TEXT_NODE && n.textContent.trim() && n.parentElement.checkVisibility() && n.parentElement.getBoundingClientRect().width > 1) {
        const range = document.createRange();
        range.selectNodeContents(n);
        return range.getBoundingClientRect().top - tabs;
      }
    }
    return null;
  });
  const before = await widths();
  const gaps = [];
  for (const name of names) {
    await tab(page, name).click();
    await expect(tab(page, name)).toHaveAttribute('aria-selected', 'true');
    expect(await widths(), name).toEqual(before);
    const [weight, bottom] = await style(tab(page, name), 'fontWeight', 'borderBottomWidth');
    expect(weight, name).toBe((await style(tab(page, names.find((n) => n !== name)), 'fontWeight'))[0]);
    expect(Number(weight), name).toBeLessThan(600);
    expect(bottom, name).toBe('3px');
    // A click shows no ring.
    expect(await tab(page, name).evaluate((e) => e.matches(':focus-visible')), name).toBe(false);
    if (name === 'Browse') await expect(browseStatus(page)).toHaveText(rootStatus);
    gaps.push(Math.round(await top()));
  }
  expect(Math.min(...gaps), String(gaps)).toBeGreaterThanOrEqual(20);
  expect(Math.max(...gaps) - Math.min(...gaps), String(gaps)).toBeLessThanOrEqual(4);
  // From the keyboard, the ring shows, inside the tab and above its
  // underline.
  await tab(page, 'Activity').focus();
  await page.keyboard.press('ArrowLeft');
  const focused = tab(page, 'Free up space');
  await expect(focused).toBeFocused();
  expect(await focused.evaluate((e) => e.matches(':focus-visible'))).toBe(true);
  const [outline, width, offset, under] = await style(focused, 'outlineStyle', 'outlineWidth', 'outlineOffset', 'borderBottomWidth');
  expect(outline).toBe('solid');
  expect(parseFloat(width)).toBeGreaterThanOrEqual(2);
  expect(-parseFloat(offset) - parseFloat(width)).toBeGreaterThanOrEqual(parseFloat(under));
  // Under forced colors, the selected tab's underline still shows and the
  // others have none.
  await page.emulateMedia({ forcedColors: 'active' });
  if (await page.evaluate(() => matchMedia('(forced-colors: active)').matches)) {
    await tab(page, 'Free up space').click();
    const line = (n) => style(tab(page, n), 'borderBottomStyle', 'borderBottomWidth', 'borderBottomColor');
    const [on, off] = [await line('Free up space'), await line('Copy')];
    const canvas = await page.evaluate(() => {
      const probe = document.body.appendChild(document.createElement('div'));
      probe.style.backgroundColor = 'Canvas';
      const color = getComputedStyle(probe).backgroundColor;
      probe.remove();
      return color;
    });
    expect(on.slice(0, 2)).toEqual(['solid', '3px']);
    expect(contrast(on[2], canvas)).toBeGreaterThanOrEqual(3);
    expect(off[2]).toBe(canvas);
  }
});

test('Copy settings save every field and keep unknown keys', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await expect(page.locator('#s-summary')).toHaveText(/^Copying to \/nowhere every 5 min, skipping logs and system files\.$/);
  await expect(page.locator('#copy-form')).toBeHidden();
  await page.getByRole('button', { name: 'Change…' }).click();
  await expect(page.getByRole('button', { name: 'Change…' })).toHaveAttribute('aria-expanded', 'true');
  await expect(field(page, 'Folder for your images')).toBeFocused();
  await expect(page.locator('#f-builtin')).toHaveText(/^\.\* · \*\.tmp · .* · \/batch_stack_tmp$/);
  await expect(page.locator('#version')).toHaveText('development');

  await field(page, 'Folder for your images').fill(app.folder);
  await page.locator('#f-int').fill('30');
  await field(page, 'Leave out').fill('Video\n\n/Preview');
  await page.getByLabel('What to copy').selectOption('Custom');
  await field(page, 'Only copy').fill('MyWorks');
  await page.getByLabel(/Skip the telescope's logs/).uncheck();
  await page.getByLabel('Start copying when the app opens').uncheck();
  await copySave(page).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(page.getByRole('button', { name: 'Change…' })).toBeFocused();
  await expect(page.locator('#copy-form')).toBeHidden();

  expect(readJSON(app.copyFile)).toEqual({
    version: 1, folder: app.folder, interval_minutes: 30,
    excludes: ['Video', '/Preview'], includes: ['MyWorks'], skip_built_in: false, start_copying: false, once: false,
    files_at_once: 4, older_than_days: 7, trust_state: false, ignore_state: false, free_excludes: [], free_includes: [], history_dir: '',
    later: { kept: true },
    // The save names the connected telescope's folder: the address saved.
    telescopes: { 'address:127.0.0.1:14450': { folder: app.folder } },
  });
  // Activity shows both files, and the copy settings saved.
  await tab(page, 'Activity').click();
  await expect(page.locator('#copy-file')).toHaveText('Copy settings: ' + app.copyFile);
  await expect(page.locator('#config-file')).toHaveText('Connection settings: ' + app.config);
  await expect(page.locator('#copy-settings')).toContainText('"interval_minutes": 30');
  // A reload shows what was saved.
  await page.reload();
  await expect(page.locator('#quit')).toBeEnabled();
  await tab(page, 'Copy').click();
  await page.getByRole('button', { name: 'Change…' }).click();
  await expect(field(page, 'Leave out')).toHaveValue('Video\n/Preview');
  await expect(page.locator('#f-int')).toHaveValue('30');
  await expect(page.getByLabel('Start copying when the app opens')).not.toBeChecked();
  // Cancel puts the saved values back.
  await page.locator('#f-int').fill('45');
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('#copy-form')).toBeHidden();
  await page.getByRole('button', { name: 'Change…' }).click();
  await expect(page.locator('#f-int')).toHaveValue('30');
});

// Copy settings… beside Preview and Change… under the summary open one
// form, and Cancel or Save gives focus back to the one pressed.
test('Copy settings… and Change… open the form and focus goes back to the one pressed', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  const settings = page.getByRole('button', { name: 'Copy settings…' });
  const change = page.getByRole('button', { name: 'Change…' });
  await expect(settings).toBeVisible();
  await expect(settings).toHaveAttribute('aria-controls', 'copy-form');
  await expect(settings).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('#copy-form')).toBeHidden();
  await settings.click();
  await expect(page.locator('#copy-form')).toBeVisible();
  await expect(field(page, 'Folder for your images')).toBeFocused();
  for (const b of [settings, change]) await expect(b).toHaveAttribute('aria-expanded', 'true');
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('#copy-form')).toBeHidden();
  await expect(settings).toBeFocused();
  for (const b of [settings, change]) await expect(b).toHaveAttribute('aria-expanded', 'false');
  await settings.click();
  await field(page, 'Folder for your images').fill(app.folder);
  await copySave(page).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(settings).toBeFocused();
  expect(readJSON(app.copyFile).folder).toBe(app.folder);
  // Change… still opens it, and takes focus back.
  await change.click();
  await expect(field(page, 'Folder for your images')).toBeFocused();
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(change).toBeFocused();
  // The header's folder opens it too, and takes focus back.
  await page.locator('#telescope-folder').click();
  await expect(field(page, 'Folder for your images')).toBeFocused();
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('#telescope-folder')).toBeFocused();
});

// While copying, Copy settings… hides with Preview, and the header's folder
// says to stop copying first; the form opens once copying stops.
test('Copy settings… hides while copying, and the folder then says to stop copying first', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  let v = copyViews.copying;
  await fakeStatus(page, () => v);
  await openCopy(page, app.url);
  await expect(page.locator('#c-title')).toHaveText(copyViews.copying.title);
  await expect(page.getByRole('button', { name: 'Copy settings…' })).toBeHidden();
  await expect(page.getByRole('button', { name: 'Preview' })).toBeHidden();
  await expect(page.getByRole('button', { name: 'Change…' })).toBeVisible();
  await tab(page, 'Activity').click();
  await page.locator('#telescope-folder').click();
  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#s-locked')).toBeVisible();
  await expect(page.locator('#s-stop')).toBeFocused();
  await expect(page.locator('#copy-form')).toBeHidden();
  v = null;
  await expect(page.locator('#copy-form')).toBeVisible();
  await expect(page.locator('#s-locked')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Copy settings…' })).toBeVisible();
  // Its Cancel gives focus back to the folder that asked for it.
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('#copy-form')).toBeHidden();
  await expect(page.locator('#telescope-folder')).toBeFocused();
});

// Closing the form while copying withdraws the folder's request too: the
// form doesn't open once copying stops. Cancel is disabled while copying,
// so the page's own functions close it here.
test('closing the form while copying withdraws the folder\'s request for it', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  const observed = await page.evaluate(() => {
    const off = { ...copyStatus.copy, on: false, state: 'off' };
    renderCopy({ ...off, on: true, state: 'copying' });
    openCopyForm($('s-change'));
    telescopeFolder();
    closeCopyForm();
    const closed = $('copy-form').hidden;
    renderCopy(off);
    return { closed, reopened: !$('copy-form').hidden };
  });
  expect(observed).toEqual({ closed: true, reopened: false });
});

// Focus goes back to the opener only while it is shown: with the folder's
// part of the line hidden, to Change….
test('focus goes to Change… when the folder that opened the form is no longer shown', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await page.locator('#telescope-folder').click();
  await expect(field(page, 'Folder for your images')).toBeFocused();
  // In one task, so a status poll can't show the folder again in between.
  const focused = await page.evaluate(() => {
    $('telescope-to').hidden = true;
    $('f-cancel').click();
    return document.activeElement.id;
  });
  expect(focused).toBe('s-change');
});

test('a second telescope is asked for its own folder beside the first, and Save refuses the first one\'s', async ({ page, startApp }) => {
  // The first telescope, known by an invented serial, has the folder; the
  // saved address is another telescope.
  const app = await startApp({
    'config.json': lan(),
    'copy.json': (folder) => ({ version: 1, folder, start_copying: false, telescopes: { 'serial:SYNTHETIC-FIRST': { folder, later: { kept: true } } } }),
  });
  await openApp(page, app.url);
  await tab(page, 'Copy').click();
  await expect(page.locator('#s-first')).toHaveText('This telescope has no folder yet. Each telescope has its own: choose one for its images, then press Save. Copying starts when you press Start copying.');
  await expect(page.locator('#c-main')).toBeHidden();
  await expect(page.locator('#s-offer')).toBeHidden();
  const suggested = path.join(app.home, 'Seestar 127.0.0.1 14450');
  await expect(field(page, 'Folder for your images')).toHaveValue(suggested);
  // Browse lists it without statuses until it has a folder.
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  await expect(frame(page).locator('#list')).not.toHaveClass(/statuses/);
  await tab(page, 'Copy').click();

  // The first telescope's folder, and one inside it, are refused.
  for (const [folder, message] of [
    [app.folder, 'Another telescope copies to this folder. Choose a folder of its own for this telescope.'],
    [path.join(app.folder, 'inner'), "This folder is inside another telescope's folder. Choose a folder outside it."],
  ]) {
    await field(page, 'Folder for your images').fill(folder);
    await copySave(page).click();
    await expect(page.locator('#f-folder-err')).toHaveText(message);
    await expect(field(page, 'Folder for your images')).toBeFocused();
  }
  expect(readJSON(app.copyFile).telescopes).toEqual({ 'serial:SYNTHETIC-FIRST': { folder: app.folder, later: { kept: true } } });

  await field(page, 'Folder for your images').fill(suggested);
  await copySave(page).click();
  await expect(page.locator('#c-next')).toBeFocused();
  await expect(page.locator('#s-first')).toBeHidden();
  const saved = readJSON(app.copyFile);
  expect(saved.folder).toBe(app.folder);
  expect(saved.telescopes).toEqual({ 'serial:SYNTHETIC-FIRST': { folder: app.folder, later: { kept: true } }, 'address:127.0.0.1:14450': { folder: suggested } });
  await tab(page, 'Browse').click();
  await expect(frame(page).locator('#list.statuses')).toBeVisible();
});

// The header's line names the route's telescope and its folder, and the
// folder, or no folder yet, opens Copy settings.
test('the header names the telescope on this network and its folder, which opens Copy settings', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': lan() });
  await openApp(page, app.url);
  const folder = page.locator('#telescope-folder');
  await expect(page.locator('#connection-text')).toHaveText(`${shareName} on this network`);
  await expect(folder).toHaveText('no folder yet');
  await expect(folder).not.toHaveAttribute('title', /./);
  await expect(page.getByRole('button', { name: 'No folder yet. Open Copy settings', exact: true })).toBeVisible();
  // One line: the folder follows the sentence, and the arrow isn't read out.
  await expect(page.locator('#connection-line')).toContainText(`${shareName} on this network→ no folder yet`);
  await expect(page.locator('#telescope-to [aria-hidden=true]')).toHaveText('→ ');
  // In a narrow window the dot stays on the sentence's first line, and the
  // arrow on the folder's.
  await page.setViewportSize({ width: 320, height: 640 });
  const onFirstLine = async (locator, of) => {
    const box = await locator.boundingBox(), line = await of.boundingBox(), middle = box.y + box.height / 2;
    expect(middle).toBeGreaterThan(line.y);
    expect(middle).toBeLessThan(line.y + 24);
  };
  await onFirstLine(page.locator('#connection-dot'), page.locator('#connection-text'));
  await onFirstLine(page.locator('#telescope-to [aria-hidden=true]'), folder);
  await page.setViewportSize({ width: 1280, height: 720 });
  await folder.click();
  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(field(page, 'Folder for your images')).toBeFocused();
  await copySave(page).click();
  await expect(page.locator('#c-next')).toBeFocused();
  // The home folder is ~, and the whole folder is the title.
  await expect(folder).toHaveText('~/Seestar');
  await expect(folder).toHaveAttribute('title', app.folder);
  await expect(folder).toHaveAccessibleName('Copy folder: ~/Seestar. Open Copy settings');
  await tab(page, 'Activity').click();
  await folder.click();
  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#copy-form')).toBeVisible();
  await expect(field(page, 'Folder for your images')).toBeFocused();
});

test('the header names a telescope saved with its serial on this network, without its address', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), lan_serial: 'SYNTHETIC-HEARD', lan_scan: 'off' } });
  await openApp(page, app.url);
  await expect(page.locator('#connection-text')).toHaveText('Seestar SYNTHETIC-HEARD on this network');
  await expect(page.locator('#telescope-folder')).toHaveText('no folder yet');
  await expect(page.locator('#app-header')).not.toContainText('127.0.0.1');
  // Copy reached it at the saved address, which the Connection tab doesn't repeat.
  await expect.poll(() => page.evaluate(() => copyStatus?.telescope?.address)).toBe(share);
  await expect(page.locator('#lan-reached')).toBeHidden();
});

// The Connection tab says where Copy last reached the saved telescope when
// that isn't the saved address, and stops once Copy reaches the saved one.
test('the Connection tab names the address Copy last reached when it differs from the saved one', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), lan_serial: 'SYNTHETIC-HEARD', lan_scan: 'off' } });
  await page.route('**/copy/api/status', async (route) => {
    const response = await route.fetch();
    const c = await response.json();
    await route.fulfill({ response, json: { ...c, telescope: { ...c.telescope, address: '192.0.2.21:4445', address_saved: false } } });
  });
  await openApp(page, app.url);
  const reached = page.locator('#lan-reached');
  await expect(reached).toHaveText('Copy last reached Seestar SYNTHETIC-HEARD at 192.0.2.21:4445.');
  await expect(reached).toBeVisible();
  await expect(page.locator('#app-header')).not.toContainText('192.0.2.21');
  await page.unroute('**/copy/api/status');
  await expect(reached).toBeHidden();
});

// With Copy off, the header's line still names the telescope saved with its
// serial, and no folder shows.
test('the header names a telescope saved with its serial while Copy is off', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), lan_serial: 'SYNTHETIC-SAVED', lan_scan: 'off' }, 'copy.json': { version: 999, folder: '/nowhere' } });
  await openApp(page, app.url);
  await expect(page.locator('#connection-text')).toHaveText('Seestar SYNTHETIC-SAVED on this network');
  await expect(page.locator('#telescope-to')).toBeHidden();
});

test('the header names the account\'s saved telescope while not connected, and never the email', async ({ page, startApp }) => {
  const app = await startApp({
    'config.json': { version: 1, bind: '127.0.0.1', idle: '0', route: 'account', email: 'synthetic@example.invalid', serial: 'SYNTHETIC-SAVED', model: 'Seestar S50 Pro', route_established: true },
    'copy.json': (folder) => ({ version: 1, folder, start_copying: false }),
  });
  await openApp(page, app.url);
  const folder = page.locator('#telescope-folder');
  await expect(page.locator('#connection-text')).toHaveText('Not connected to Seestar S50 Pro SYNTHETIC-SAVED');
  await expect(page.locator('#connection-dot')).toHaveAttribute('data-tone', 'off');
  await expect(folder).toHaveText('~/Seestar');
  await expect(folder).toHaveAttribute('title', app.folder);
  await expect(page.locator('#app-header')).not.toContainText('@');
  await folder.click();
  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#copy-form')).toBeVisible();
  await expect(field(page, 'Folder for your images')).toBeFocused();
});

// A route saved while Copy's status lags names the new telescope alone,
// never with the old one's folder, until Copy's status of it is read: its
// poll failing, then answering late, then caught up.
test('the header shows Copy\'s folder only for the telescope the app status names', async ({ page, startApp }) => {
  const app = await startApp({
    'config.json': { ...lan(), lan_serial: 'SYNTHETIC-FIRST', lan_scan: 'off', email: 'synthetic@example.invalid', serial: 'SYNTHETIC-SAVED', model: 'Seestar S50 Pro' },
    'copy.json': (folder) => ({ version: 1, folder, start_copying: false, telescopes: { 'serial:SYNTHETIC-FIRST': { folder } } }),
  });
  await openApp(page, app.url);
  const folder = page.locator('#telescope-folder');
  await expect(page.locator('#connection-text')).toHaveText('Seestar SYNTHETIC-FIRST on this network');
  await expect(folder).toHaveText('~/Seestar');
  await page.route('**/copy/api/status', (route) => route.abort());
  await page.evaluate(() => api('route', { settings: { route: 'account' } }));
  const saved = 'Not connected to Seestar S50 Pro SYNTHETIC-SAVED';
  await expect(page.locator('#connection-text')).toHaveText(saved);
  await expect(page.locator('#lost')).toBeVisible();
  await expect(page.locator('#telescope-to')).toBeHidden();
  let release;
  const held = new Promise((resolve) => (release = resolve));
  let asked;
  const late = new Promise((resolve) => (asked = resolve));
  await page.unroute('**/copy/api/status');
  await page.route('**/copy/api/status', async (route) => { asked(); await held; await route.continue(); });
  await late;
  await expect(page.locator('#connection-text')).toHaveText(saved);
  await expect(page.locator('#telescope-to')).toBeHidden();
  release();
  await expect(page.locator('#lost')).toBeHidden();
  await expect(page.locator('#connection-text')).toHaveText(saved);
  await expect(folder).toHaveText('no folder yet');
  await expect(folder).not.toHaveAttribute('title', /./);
});

// Away to another route and back to the same telescope at the same
// address, the header shows nothing of a Copy status read before, kept or
// answering late, until Copy's status is read again; one answering after
// that changes nothing in it.
const returns = {
  kept: 'back at the same telescope, the header shows no kept Copy status from before',
  late: 'back at the same telescope, the header shows no late Copy status from before',
  afterFresh: 'back at the same telescope, a Copy status from before answering after a fresh one leaves the header as it was',
};
for (const [order, title] of Object.entries(returns)) {
  test(title, async ({ page, startApp }) => {
    const app = await startApp({
      'config.json': { ...lan(), lan_serial: 'SYNTHETIC-FIRST', lan_scan: 'off', email: 'synthetic@example.invalid', serial: 'SYNTHETIC-SAVED', model: 'Seestar S50 Pro' },
      'copy.json': (folder) => ({ version: 1, folder, start_copying: false, telescopes: { 'serial:SYNTHETIC-FIRST': { folder } } }),
    });
    await openApp(page, app.url);
    const line = page.locator('#connection-text');
    const folder = page.locator('#telescope-folder');
    const lastReached = page.locator('#lan-reached');
    const name = 'Seestar SYNTHETIC-FIRST on this network';
    await expect(line).toHaveText(name);
    await expect(folder).toHaveText('~/Seestar');
    const address = await page.evaluate(() => state.settings.lan_address);
    // Copy's status as an earlier session left it: another address reached
    // and another folder.
    const old = async (route, more = {}) => {
      const response = await route.fetch();
      const c = await response.json();
      await route.fulfill({ response, json: { ...c, ...more, settings: { ...c.settings, folder: '/synthetic/Old' }, telescope: { ...c.telescope, address: '192.0.2.99:4445', address_saved: false, folder: '~/Old' } } });
    };
    const away = async () => {
      await page.evaluate(async () => { await api('route', { settings: { route: 'account' } }); await refresh(); });
      await expect(line).toHaveText('Not connected to Seestar S50 Pro SYNTHETIC-SAVED');
      await expect(page.locator('#telescope-to')).toBeHidden();
    };
    const back = async () => {
      await page.evaluate(async (lan_address) => { await api('route', { settings: { route: 'lan', lan_address } }); await refresh(); }, address);
      await expect(line).toHaveText(name);
      await expect(page.locator('#telescope-to')).toBeHidden();
      await expect(lastReached).toBeHidden();
    };
    if (order === 'kept') {
      await page.route('**/copy/api/status', old);
      await expect(lastReached).toHaveText('Copy last reached Seestar SYNTHETIC-FIRST at 192.0.2.99:4445.');
      await expect(line).toHaveText(name);
      await expect(folder).toHaveAttribute('title', '/synthetic/Old');
      // Copy's status is kept while its poll fails.
      await page.unroute('**/copy/api/status');
      await page.route('**/copy/api/status', (route) => route.abort());
      await away();
      await back();
    } else {
      let release, asked;
      const held = new Promise((resolve) => (release = resolve));
      const reached = new Promise((resolve) => (asked = resolve));
      await page.route('**/copy/api/status', async (route) => { asked(); await held; await old(route, { set_up: false }); }, { times: 1 });
      await reached;
      await away();
      await back();
      if (order === 'afterFresh') {
        // The poll waits on the held read, so read Copy's status here.
        await page.evaluate(() => copyRefresh());
        await expect(line).toHaveText(name);
        await expect(folder).toHaveText('~/Seestar');
        await expect(folder).toHaveAttribute('title', app.folder);
      }
      // The polls after it fail, so the late status is the last one read.
      await page.route('**/copy/api/status', (route) => route.abort());
      release();
      await expect.poll(() => page.evaluate(() => copyStatus.telescope.address)).toBe('192.0.2.99:4445');
      if (order === 'afterFresh') {
        await expect(line).toHaveText(name);
        await expect(folder).toHaveText('~/Seestar');
        await expect(folder).toHaveAttribute('title', app.folder);
        await expect(lastReached).toBeHidden();
        return;
      }
    }
    await expect(line).toHaveText(name);
    await expect(page.locator('#telescope-to')).toBeHidden();
    await expect(lastReached).toBeHidden();
    await expect(folder).not.toHaveAttribute('title', /./);
    await page.unroute('**/copy/api/status');
    await expect(line).toHaveText(name);
    await expect(folder).toHaveText('~/Seestar');
    await expect(folder).toHaveAttribute('title', app.folder);
  });
}

test('a serial heard at an address with a folder is offered that folder, and nothing moves until it is chosen', async ({ page, startApp }) => {
  const app = await startApp({
    'config.json': { ...lan(), lan_serial: 'SYNTHETIC-HEARD' },
    'copy.json': (folder) => ({ version: 1, folder, start_copying: false, telescopes: { 'address:127.0.0.1:14450': { folder } } }),
  });
  await openApp(page, app.url);
  await tab(page, 'Copy').click();
  await expect(page.locator('#s-offer-text')).toHaveText(`This telescope's address is now known as Seestar SYNTHETIC-HEARD. If it is the same telescope, it can keep the folder saved for the address, ${app.folder}. Nothing changes until you choose it.`);
  await expect(page.locator('#s-first')).toHaveText(/^This telescope has no folder yet\./);
  const before = fs.readFileSync(app.copyFile, 'utf8');
  await page.waitForTimeout(500);
  expect(fs.readFileSync(app.copyFile, 'utf8')).toBe(before);

  await page.getByRole('button', { name: "Use the address's folder" }).click();
  await expect(page.locator('#s-offer')).toBeHidden();
  await expect(page.locator('#c-main')).toBeVisible();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(page.locator('#s-summary')).toContainText('Copying to ' + app.folder + ' ');
  expect(readJSON(app.copyFile).telescopes).toEqual({ 'serial:SYNTHETIC-HEARD': { folder: app.folder } });
});

test('Copy settings field errors show under each field and nothing is saved', async ({ page, startApp }) => {
  const files = setUp('/nowhere');
  const app = await startApp(files);
  await openCopy(page, app.url);
  await page.getByRole('button', { name: 'Change…' }).click();
  await page.locator('#f-int').fill('0');
  await field(page, 'Leave out').fill('Video\n\n[Pp]review\n!Video');
  await page.getByLabel('What to copy').selectOption('Custom');
  await field(page, 'Only copy').fill('MyWorks/ ');
  await copySave(page).click();

  await expect(page.locator('#f-int-err')).toHaveText('Enter a whole number of minutes from 1 to 1440.');
  await expect(page.locator('#f-ex-err')).toHaveText('Line 4: a leading ! is not supported; excludes always win, so put the files to keep in Only copy. Correct this pattern and save again.');
  await expect(page.locator('#f-in-err')).toHaveText('Line 1: trailing spaces are ignored in .gitignore syntax; remove them, or write \\ (a backslash, then a space) to match one. Correct this pattern and save again.');
  await expect(page.locator('#f-int')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#f-int')).toBeFocused();
  await expect(page.locator('#f-saved')).toHaveText('');
  expect(readJSON(app.copyFile)).toEqual(files['copy.json']);

  // The first field in error takes the focus: Only copy is above Leave out.
  await page.locator('#f-int').fill('10');
  await copySave(page).click();
  await expect(field(page, 'Only copy')).toBeFocused();
  await expect(page.locator('#f-int-err')).toBeHidden();
  await expect(page.locator('#f-ex-err')).toBeVisible();
  await expect(page.locator('#f-int')).not.toHaveAttribute('aria-invalid', 'true');

  await field(page, 'Leave out').fill('Video\n\n[Pp]review\nMyWorks/**/*.mp4');
  await copySave(page).click();
  await expect(field(page, 'Only copy')).toBeFocused();
  await expect(page.locator('#f-ex-err')).toBeHidden();
});

// What to copy is never saved: it is the option Only copy's patterns make,
// and each pick fills or empties Only copy.
test('What to copy fills or empties Only copy, and a reload shows the option the saved patterns make', async ({ page, startApp }) => {
  const files = setUp('/nowhere');
  files['copy.json'].includes = [];
  const app = await startApp(files);
  await openCopy(page, app.url);
  const what = page.getByLabel('What to copy');
  const only = field(page, 'Only copy');
  const open = async () => {
    await page.getByRole('button', { name: 'Change…' }).click();
    await expect(field(page, 'Folder for your images')).toBeFocused();
  };
  const save = async () => {
    await copySave(page).click();
    await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  };
  // A saved empty Only copy is Everything, and saves as it was.
  await open();
  await expect(what).toHaveValue('all');
  await expect(only).toBeHidden();
  await expect(page.locator('#f-what-help')).toBeHidden();
  await field(page, 'Folder for your images').fill(app.folder);
  await save();
  const saved = readJSON(app.copyFile);
  expect(saved).toMatchObject({ version: 1, includes: [] });

  for (const [option, value, includes, shown] of [
    ['FITS files only', 'fits', ['*.fit', '*.fits'], 'fits'],
    ['Custom', 'custom', ['*.fit', '*.fits'], 'fits'],
    ['Everything', 'all', [], 'all'],
  ]) {
    await open();
    await what.selectOption(option);
    if (value === 'custom') await expect(only).toHaveValue(includes.join('\n'));
    await save();
    expect(readJSON(app.copyFile), option).toEqual({ ...saved, includes });
    await openCopy(page, app.url);
    await open();
    await expect(what, option).toHaveValue(shown);
    await expect(only).toBeVisible({ visible: shown === 'custom' });
    await page.getByRole('button', { name: 'Cancel' }).click();
  }
  // Custom keeps what is typed, and the patterns' case is their own.
  await open();
  await what.selectOption('Custom');
  await expect(only).toBeVisible();
  // Only copy sits under What to copy, above Leave out, as the tab
  // order goes too.
  expect(await page.evaluate(() => ['f-what', 'f-in', 'f-ex'].map(id => document.getElementById(id))
    .every((el, i, all) => i === 0 || all[i - 1].compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING))).toBe(true);
  await expect(only).toHaveValue('');
  await only.fill('*.FIT\n*.fits');
  await save();
  expect(readJSON(app.copyFile).includes).toEqual(['*.FIT', '*.fits']);
  await openCopy(page, app.url);
  await open();
  await expect(what).toHaveValue('custom');
  await expect(only).toHaveValue('*.FIT\n*.fits');
  // A pattern keeps its spaces, as selection does, so ' *.fit', which
  // selects no ordinary .fit file, comes back as Custom.
  await only.fill(' *.fit\n*.fits');
  await save();
  expect(readJSON(app.copyFile).includes).toEqual([' *.fit', '*.fits']);
  await openCopy(page, app.url);
  await open();
  await expect(what).toHaveValue('custom');
  await expect(only).toHaveValue(' *.fit\n*.fits');
  // Picking an option fills Only copy, and Custom then keeps it.
  await what.selectOption('FITS files only');
  await expect(only).toBeHidden();
  await what.selectOption('Custom');
  await expect(only).toHaveValue('*.fit\n*.fits');
  await what.selectOption('Everything');
  await what.selectOption('Custom');
  await expect(only).toHaveValue('');
});

test('Files at once defaults to 4, saves 1 to 4 and refuses anything else', async ({ page, startApp }) => {
  const files = setUp('/nowhere');
  const app = await startApp(files);
  await openCopy(page, app.url);
  await page.getByRole('button', { name: 'Change…' }).click();
  // A copy.json without it shows 4, with the advice for the account beside it.
  const count = page.getByLabel('Files at once');
  await expect(count).toHaveValue('4');
  await expect(count).toHaveAttribute('min', '1');
  await expect(count).toHaveAttribute('max', '4');
  await expect(page.locator('#f-files-help')).toHaveText(/^Copying runs this many files at once, 1 to 4\. Through Telescope Network, .* lower this to 1 or 2\.$/);
  await expect(count).toHaveAccessibleDescription(/lower this to 1 or 2/);

  for (const value of ['5', '0', '2.5']) {
    await count.fill(value);
    await copySave(page).click();
    await expect(page.locator('#f-files-err')).toHaveText('Enter a whole number from 1 to 4.');
    await expect(count).toHaveAttribute('aria-invalid', 'true');
    await expect(count).toBeFocused();
    await expect(page.locator('#f-saved')).toHaveText('');
    expect(readJSON(app.copyFile)).toEqual(files['copy.json']);
  }

  await count.fill('2');
  await copySave(page).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(page.locator('#s-summary')).toHaveText('Copying to /nowhere every 5 min, 2 files at once, skipping logs and system files.');
  expect(readJSON(app.copyFile)).toMatchObject({ files_at_once: 2, later: { kept: true } });
  // A reload shows what was saved, without the error.
  await page.reload();
  await expect(page.locator('#quit')).toBeEnabled();
  await tab(page, 'Copy').click();
  await page.getByRole('button', { name: 'Change…' }).click();
  await expect(count).toHaveValue('2');
  await expect(page.locator('#f-files-err')).toBeHidden();
  await expect(count).not.toHaveAttribute('aria-invalid', 'true');
});

// contrast is the WCAG contrast ratio of two computed rgb() colors.
function contrast(a, b) {
  const lum = (c) => {
    const [r, g, b2] = c.match(/\d+/g).slice(0, 3).map((v) => {
      const x = v / 255;
      return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b2;
  };
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// An error Browse showed at one address goes with a change of route:
// Browse loads again through the new one when it is next shown.
test('Browse follows a change of route', async ({ page, startApp }) => {
  const app = await startApp({ ...setUp('/nowhere'), 'config.json': lan(refused) });
  await openCopy(page, app.url);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toContainText('Select Share or a parent folder above', { timeout: 30000 });
  await tab(page, 'Connection').click();
  await field(page, 'Telescope address').fill(share);
  await page.locator('#lan-save').click();
  await expect(lanResult(page)).toHaveText(`Saved. ${share} is reachable.`);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
});

// A Browse connect that fails is in Activity as a Browse row, and the
// copied diagnostic summary holds it in failures, gives the trail newest
// first, and gives the error's text, which names the address dialed.
test('the diagnostic summary holds a failing Browse connect', async ({ page, startApp }) => {
  const app = await startApp({ ...setUp('/nowhere'), 'config.json': lan(refused) });
  await page.addInitScript(() => {
    window.copied = [];
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (t) => { window.copied.push(t); } } });
  });
  await openCopy(page, app.url);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toContainText('Select Share or a parent folder above', { timeout: 30000 });
  await tab(page, 'Activity').click();
  await expect(page.locator('#events li[data-source="browse"]').first()).toContainText("Browse  Browse couldn't reach the telescope.");
  await page.locator('#copy').click();
  await expect.poll(() => page.evaluate(() => window.copied.length)).toBe(1);
  const text = await page.evaluate(() => window.copied[0]);
  const summary = JSON.parse(text);
  expect(summary.failures.map((f) => f.source + '/' + f.op)).toContain('browse/connect');
  expect(summary.failures.find((f) => f.op === 'connect')).toMatchObject({ code: 'browse_connect_failed', phase: 'connect' });
  expect(summary.error_code).not.toBe('');
  const times = summary.events.map((e) => Date.parse(e.time));
  expect(times.length).toBeGreaterThan(1);
  expect(times[0]).toBeGreaterThanOrEqual(times[times.length - 1]);
  const failedAt = summary.events.findIndex((e) => e.code === 'browse_connect_failed');
  const openedAt = summary.events.findIndex((e) => e.message === 'App opened.');
  expect(failedAt).toBeGreaterThanOrEqual(0);
  expect(openedAt).toBeGreaterThan(failedAt);
  expect(summary.events[failedAt].error).toMatch(/127\.0\.0\.1/);
  expect(summary.failures.find((f) => f.op === 'connect').error).toMatch(/127\.0\.0\.1/);
});

// While the controls are lost, Browse keeps its loaded view but takes no
// click, key or focus, its folders and Save links included, until a poll
// succeeds.
test('a stale Browse takes no input until the controls come back', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': lan() });
  await openApp(page, app.url);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  const browse = page.locator('#b-frame');
  const myWorks = frame(page).getByRole('link', { name: 'MyWorks/', exact: true });
  // Focus is in Browse when the controls go.
  await myWorks.focus();
  let asked = 0;
  page.on('request', (r) => { if (r.url().includes('/browse/api/')) asked++; });
  await page.route('**/api/status', (route) => route.abort());
  await expect(page.locator('#lost')).toBeVisible();
  await expect(browse).toHaveAttribute('data-stale', '');
  await expect(browseStatus(page)).toHaveText(rootStatus);
  const box = await myWorks.boundingBox();
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  await page.keyboard.press('Enter');
  await page.keyboard.press('Space');
  await page.waitForTimeout(500);
  expect(asked).toBe(0);
  await expect(browseStatus(page)).toHaveText(rootStatus);
  for (let i = 0; i < 40; i++) {
    await page.keyboard.press('Tab');
    expect(await page.evaluate(() => document.activeElement?.id)).not.toBe('b-frame');
  }
  await page.unroute('**/api/status');
  await expect(page.locator('#lost')).toBeHidden();
  await myWorks.click();
  await expect(frame(page).locator('#crumbs a[aria-current="page"]')).toHaveText('MyWorks');
});

// After the controls come back, the copy tabs wait for a fresh status of
// their own before they turn back on; the connection doesn't wait.
test('the copy tabs stay stale until copy status is read again', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await expect(page.locator('#c-start')).toBeEnabled();
  await page.route('**/api/status', (route) => route.abort());
  await expect(page.locator('#lost')).toBeVisible();
  let release;
  const barrier = new Promise((resolve) => (release = resolve));
  await page.route('**/copy/api/status', async (route) => { await barrier; await route.continue(); });
  await page.unroute('**/api/status');
  await expect(page.locator('#lost')).toBeHidden();
  await expect(page.locator('#lan-options')).toBeEnabled();
  await expect(page.locator('#c-box')).toHaveAttribute('data-stale', '');
  await expect(page.locator('#c-start')).toBeDisabled();
  release();
  await expect(page.locator('#c-start')).toBeEnabled();
  await expect(page.locator('#c-box')).not.toHaveAttribute('data-stale');
});

// The frame is named for assistive technology without a title, which
// would show as a tooltip over the list.
test("Browse's frame is named The telescope's files, with no title", async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  const browse = page.locator('#b-frame');
  await expect(browse).not.toHaveAttribute('title');
  await expect(browse).toHaveAccessibleName("The telescope's files");
  await expect(page.getByRole('tabpanel', { name: 'Browse' }).locator('iframe[title]')).toHaveCount(0);
});

// Browse is one of the app's tabs, not a second app in a frame: the
// app's type, page, text, links, focus ring and buttons, light and dark.
test("Browse takes the app's look", async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  const browse = frame(page);
  expect(await browse.locator('html').evaluate(() => document.title)).toBe('Browse — seestar-wormhole');
  const look = (loc, ...props) => loc.evaluate((e, props) => props.map((p) => getComputedStyle(e)[p]), props);
  const type = ['fontFamily', 'fontSize', 'lineHeight', 'color'];
  const button = ['color', 'backgroundColor', 'borderTopColor', 'borderTopWidth', 'borderTopLeftRadius', 'paddingTop', 'paddingLeft'];
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    expect(await look(browse.locator('body'), ...type), colorScheme).toEqual(await look(page.locator('main'), ...type));
    expect(await look(browse.locator('body'), 'backgroundColor'), colorScheme).toEqual(await look(page.locator('html'), 'backgroundColor'));
    expect(await look(browse.getByRole('link', { name: 'MyWorks/' }), 'color'), colorScheme)
      .toEqual(await look(page.locator('footer a').first(), 'color'));
    expect(await look(browse.locator('#vclose'), ...button), colorScheme).toEqual(await look(page.locator('#quit'), ...button));
  }
  // The keyboard's focus ring, as the app draws it: 3 px in its focus
  // color, 3 px out.
  await browse.getByRole('link', { name: 'MyWorks/' }).focus();
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Tab');
  const focused = browse.locator(':focus');
  expect(await focused.evaluate((e) => e.matches(':focus-visible'))).toBe(true);
  expect(await look(focused, 'outlineStyle', 'outlineWidth', 'outlineOffset', 'outlineColor'))
    .toEqual(['solid', '3px', '3px', (await look(page.locator('footer a').first(), 'color'))[0]]);
});

test('Browse marks what Leave out leaves out, with a key', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  // The key is a list: each status's badge, then what it means.
  const key = frame(page).getByRole('list', { name: 'Status key' });
  await expect(key).toBeVisible();
  // It sits in a box of its own, headed Key, apart from the table.
  const box = frame(page).locator('#key');
  await expect(box.getByRole('heading', { name: 'Key', exact: true })).toBeVisible();
  const table = await frame(page).locator('#list').boundingBox();
  expect((await box.boundingBox()).y - (table.y + table.height)).toBeGreaterThanOrEqual(16);
  await expect(key.getByRole('listitem')).toHaveText(['new: not copied yet', 'copied: already copied', 'changed: differs from the copy', 'left out: not copied by the selection']);
  const keyBadge = (name) => key.getByRole('listitem').filter({ hasText: name + ':' }).locator('.badge');
  const badge = (name) => frame(page).locator('#list tbody tr').filter({ has: frame(page).getByText(name, { exact: true }) }).locator('.badge');
  // Its badges are drawn as the list's are, and each meaning follows its
  // badge on the same line.
  const look = (loc) => loc.evaluate((e) => { const s = getComputedStyle(e); return [s.color, s.borderTopStyle, s.borderTopWidth, s.borderRadius, s.fontSize, s.paddingLeft]; });
  await expect(keyBadge('new')).toHaveText('new');
  expect(await look(keyBadge('new'))).toEqual(await look(badge('empty.txt')));
  for (const item of await key.getByRole('listitem').all()) {
    const [b, i] = [await item.locator('.badge').boundingBox(), await item.boundingBox()];
    expect(i.height).toBeLessThan(2 * b.height);
  }
  await expect(badge('empty.txt')).toHaveText('new');
  await expect(frame(page).locator('#list .badge.left-out')).toHaveCount(0);

  await tab(page, 'Copy').click();
  await page.getByRole('button', { name: 'Change…' }).click();
  await field(page, 'Leave out').fill('empty.txt\nLong names');
  await copySave(page).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await tab(page, 'Browse').click();
  await expect(browseStatus(page)).toHaveText(rootStatus);
  await expect(badge('empty.txt')).toHaveText('left out');
  await expect(badge('empty.txt')).toHaveClass('badge left-out');
  await expect(badge('Long names/')).toHaveText('left out');
  await expect(badge('00-notes.txt')).toHaveText('new');
  await expect(badge('MyWorks/')).toHaveCount(0);
  await expect(key).toBeVisible();
  expect(await look(keyBadge('left out'))).toEqual(await look(badge('empty.txt')));

  // Muted against new: a dashed border, and readable in light and dark.
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    const style = (loc, prop) => loc.evaluate((e, p) => getComputedStyle(e)[p], prop);
    const bg = await style(frame(page).locator('body'), 'backgroundColor');
    const muted = await style(badge('empty.txt'), 'color');
    expect(await style(badge('empty.txt'), 'borderTopStyle')).toBe('dashed');
    expect(await style(badge('00-notes.txt'), 'borderTopStyle')).toBe('solid');
    expect(muted).not.toBe(await style(badge('00-notes.txt'), 'color'));
    expect(contrast(muted, bg), colorScheme).toBeGreaterThanOrEqual(4.5);
    // The key's box is bordered and tinted, and its muted text stays
    // readable on the tint.
    const tint = await style(box, 'backgroundColor');
    expect(tint, colorScheme).not.toBe(bg);
    expect(await style(box, 'borderTopStyle')).toBe('solid');
    expect(contrast(await style(box, 'borderTopColor'), tint), colorScheme).toBeGreaterThan(1.3);
    expect(contrast(await style(keyBadge('left out'), 'color'), tint), colorScheme).toBeGreaterThanOrEqual(4.5);
    expect(contrast(await style(key, 'color'), tint), colorScheme).toBeGreaterThanOrEqual(4.5);
  }
});

test('Save is off until its answer is in', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await page.getByRole('button', { name: 'Change…' }).click();
  let posts = 0;
  let release;
  const held = new Promise((resolve) => (release = resolve));
  await page.route('**/copy/api/settings', async (route) => {
    posts++;
    await held;
    await route.continue();
  });
  // A failure here still lets the held request go, so teardown can drain it.
  try {
    await copySave(page).click();
    await expect(copySave(page)).toBeDisabled();
    // Enter in a field would submit again.
    await page.locator('#f-int').press('Enter');
  } finally {
    release();
  }
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(copySave(page)).toBeEnabled();
  expect(posts).toBe(1);
});

test('the app page refuses a request without its token', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  const base = app.url.split('#')[0];
  await page.goto(base);
  const codes = await page.evaluate(async () => {
    const post = (url) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    return Promise.all([fetch('api/status'), fetch('copy/api/status'), post('copy/api/settings'), post('api/route')]).then((rs) => rs.map((r) => r.status));
  });
  expect(codes).toEqual([403, 403, 403, 403]);
  await expect(page.locator('#lost-text')).toHaveText('Connection to the controls was lost. The proxy may still be running. Reopen the app to reconnect.');
  await expect(page.locator('#lost')).toBeVisible();
  await expect(page.locator('#notice')).toBeHidden();
  for (const name of ['Copy', 'Browse', 'Free up space']) await expect(tab(page, name)).toBeDisabled();
});

test('Copy history shows its file, with a Copy button, and an edited folder says to save first', async ({ page, startApp }) => {
  // window.copied records what the page copies.
  await page.addInitScript(() => {
    window.copied = [];
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (s) => window.copied.push(s) } });
  });
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await page.getByText('Copy history', { exact: true }).click();
  const history = page.locator('#h-path');
  await expect(history).toHaveText(/^\/.*[/\\]seestar-wormhole[/\\]copy-history[/\\]sync[/\\][0-9a-f]{64}\.jsonl$/);
  const first = await history.textContent();
  await page.locator('#h-clip').click();
  await expect(page.locator('#h-clip')).toHaveText('Copied');
  await expect(page.locator('#h-result')).toHaveText('Copy history path copied.');
  expect(await page.evaluate(() => window.copied)).toEqual([first]);
  await page.evaluate(() => (navigator.clipboard.writeText = async () => { throw new Error('denied'); }));
  await page.locator('#h-clip').click();
  await expect(page.locator('#h-result')).toHaveText("Couldn't copy the path. Select it and copy it manually.");

  // A folder from the window's picker is an edit too.
  await page.getByRole('button', { name: 'Change…' }).click();
  await page.route('**/copy/api/folder', (route) => route.fulfill({ json: { folder: app.folder } }));
  await page.locator('#f-choose').dispatchEvent('click');
  await expect(field(page, 'Folder for your images')).toHaveValue(app.folder);
  await expect(page.locator('#h-saved')).toBeHidden();

  // An edited folder has no history to show until it is saved.
  await field(page, 'Folder for your images').fill(app.folder);
  await expect(page.locator('#h-unsaved')).toHaveText("Save to see this folder's copy history.");
  await expect(page.locator('#h-saved')).toBeHidden();
  await field(page, 'Folder for your images').fill('/nowhere');
  await expect(page.locator('#h-saved')).toBeVisible();
  await field(page, 'Folder for your images').fill(app.folder);
  await copySave(page).click();
  await expect(page.locator('#h-saved')).toBeVisible();
  await expect(history).not.toHaveText(first);
  await expect(history).toHaveText(/\.jsonl$/);
});

test('Copy history folder is empty for the default, refuses a relative path, and a saved one holds the history', async ({ page, startApp }) => {
  const app = await startApp(setUp('/nowhere'));
  await openCopy(page, app.url);
  await page.getByText('Copy history', { exact: true }).click();
  const history = page.locator('#h-path');
  await expect(history).toHaveText(/^\/.*[/\\]seestar-wormhole[/\\]copy-history[/\\]sync[/\\][0-9a-f]{64}\.jsonl$/);
  const first = await history.textContent();
  await page.getByRole('button', { name: 'Change…' }).click();
  const hist = field(page, 'Copy history folder');
  await expect(hist).toHaveValue('');
  // The placeholder is the default folder, the one the history file is in.
  await expect(hist).toHaveAttribute('placeholder', first.replace(/[/\\]sync[/\\][0-9a-f]{64}\.jsonl$/, ''));
  await expect(page.locator('#f-hist-help')).toContainText('A different folder starts a fresh history');
  // The help names no folder of its own: the default differs by system.
  await expect(page.locator('#f-hist-help')).toContainText('Leave empty to use the default folder shown in this field, which seestar-wormhole-cli sync uses too.');
  await expect(page.locator('#f-hchoose')).toBeHidden();
  // Each Choose… names its field for a screen reader's list of buttons.
  for (const [name, id] of [['Choose folder for your images', 'f-choose'], ['Choose copy history folder', 'f-hchoose']]) {
    await expect(page.getByRole('button', { name, exact: true, includeHidden: true })).toHaveAttribute('id', id);
  }

  await hist.fill('history');
  await expect(page.locator('#h-unsaved')).toBeVisible();
  await copySave(page).click();
  await expect(page.locator('#f-hist-err')).toHaveText('Use a full path to a folder outside the folder for your images, or leave it empty for the default.');
  await expect(hist).toHaveAttribute('aria-invalid', 'true');
  await expect(hist).toBeFocused();
  expect(readJSON(app.copyFile).history_dir).toBeUndefined();

  // A folder from the window's picker fills the field.
  const chosen = path.join(app.home, 'history');
  await page.route('**/copy/api/folder', (route) => route.fulfill({ json: { folder: chosen } }));
  await page.locator('#f-hchoose').dispatchEvent('click');
  await expect(hist).toHaveValue(chosen);
  await copySave(page).click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await expect(page.locator('#f-hist-err')).toBeHidden();
  await expect(page.locator('#h-saved')).toBeVisible();
  await expect(history).toHaveText(new RegExp('^' + chosen.replace(/[\\^$.*+?()[\]{}|]/g, '\\$&') + '[/\\\\]sync[/\\\\][0-9a-f]{64}\\.jsonl$'));
  expect(readJSON(app.copyFile)).toMatchObject({ version: 1, folder: '/nowhere', history_dir: chosen, later: { kept: true } });
});

// remoteAdvanced opens the remote route's Advanced, with its Services table
// and added ports.
async function remoteAdvanced(page) {
  await page.getByLabel('Remotely through Telescope Network').check();
  await page.locator('#advanced summary').click();
  await expect(page.locator('#ports input').first()).toBeVisible();
}
const addedRow = (page, n) => ({
  remote: field(page, `Seestar port for added port ${n}`),
  local: field(page, `Local port for added port ${n}`),
  remove: page.getByRole('button', { name: `Remove added port ${n}`, exact: true }),
});

test('Add a port adds, edits, removes and saves a row', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), route: 'account', email: 'synthetic@example.invalid', ports: [{ local: 7001, device: 7000 }] } });
  await openApp(page, app.url);
  await remoteAdvanced(page);
  // The Services rows are in Seestar port order.
  await expect(page.locator('#ports tbody td:nth-child(2)')).toHaveText(['80', '445', '4350', '4361', '4554', '4555', '4700', '4800', '4801', '4804', '32323']);
  // The saved added port loads as a row of its own.
  await expect(addedRow(page, 1).remote).toHaveValue('7000');
  await expect(addedRow(page, 1).local).toHaveValue('7001');

  await page.getByRole('button', { name: 'Add a port', exact: true }).click();
  const second = addedRow(page, 2);
  await expect(second.remote).toBeFocused();
  // The local port starts as the Seestar port, and keeps an edit.
  await second.remote.fill('5000');
  await expect(second.local).toHaveValue('5000');
  await second.local.fill('5001');
  await second.remote.fill('5002');
  await expect(second.local).toHaveValue('5001');

  // Removing the first row renumbers the second.
  await addedRow(page, 1).remove.click();
  await expect(page.getByRole('button', { name: 'Add a port', exact: true })).toBeFocused();
  await expect(addedRow(page, 1).remote).toHaveValue('5002');
  await expect(addedRow(page, 2).remote).toHaveCount(0);

  // Connect sends the row in ports, and the running table lists it.
  await field(page, 'Password').fill('synthetic-password');
  const start = page.waitForRequest((r) => r.url().endsWith('/api/start'));
  await page.locator('#start').click();
  const sent = (await start).postDataJSON().settings.ports;
  expect(sent).toHaveLength(12);
  expect(sent[11]).toEqual({ device: 5002, local: 5001 });
  await expect(page.locator('#running')).toBeVisible();
  await page.locator('#running-ports-box summary').click();
  const added = page.locator('#running-ports tbody tr').filter({ hasText: 'Added' });
  await expect(added).toHaveCount(1);
  await expect(added.locator('td')).toHaveText(['5001', '5002', 'Added']);
  await page.locator('#start').click();
  await expect(page.locator('#settings')).toBeVisible();
});

test('added rows put the local port first and rows using another local port are shaded', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), route: 'account', email: 'synthetic@example.invalid', ports: [{ local: 7001, device: 7000 }] } });
  await openApp(page, app.url);
  await remoteAdvanced(page);
  await expect(page.locator('#added-ports th')).toHaveText(['Local port', 'Seestar port', '']);
  await expect(page.locator('#added-ports tbody tr').first().locator('td').nth(0).locator('input')).toHaveValue('7001');
  await expect(page.locator('#added-ports tbody tr').first().locator('td').nth(1).locator('input')).toHaveValue('7000');
  await expect(page.locator('#ports-shade-help')).toHaveText('Shaded rows use a different port on this computer than on the Seestar.');

  const serviceRow = (port) => page.locator('#ports tbody tr').filter({ has: page.locator(`#port-${port}`) });
  // SMB is 4445 on this computer; Seestar control and the firmware updater
  // use their own ports.
  await expect(serviceRow(445)).toHaveClass('remapped');
  await expect(serviceRow(4700)).not.toHaveClass('remapped');
  await expect(page.locator('#added-ports tbody tr').first()).toHaveClass('remapped');
  const shade = await serviceRow(445).locator('td').first().evaluate((e) => getComputedStyle(e).backgroundColor);
  const plain = await serviceRow(4700).locator('td').first().evaluate((e) => getComputedStyle(e).backgroundColor);
  expect(shade).not.toBe(plain);
  // The shading follows edits, and an off row isn't shaded.
  await page.locator('#port-4700').fill('4701');
  await expect(serviceRow(4700)).toHaveClass('remapped');
  await page.locator('#port-445').fill('');
  await expect(serviceRow(445)).not.toHaveClass('remapped');
  await addedRow(page, 1).local.fill('7000');
  await expect(page.locator('#added-ports tbody tr').first()).not.toHaveClass('remapped');
});

test('port rows show their own errors as you edit, and a refusal on its row', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), route: 'account', email: 'synthetic@example.invalid' } });
  await openApp(page, app.url);
  await remoteAdvanced(page);
  await field(page, 'Password').fill('synthetic-password');
  await page.getByRole('button', { name: 'Add a port', exact: true }).click();
  const row = addedRow(page, 1);
  let starts = 0;
  page.on('request', (r) => { if (r.url().endsWith('/api/start')) starts++; });
  // shows checks the field's own error, which its description includes.
  const shows = async (input, message) => {
    await expect(input).toHaveAttribute('aria-invalid', 'true');
    await expect(input).toHaveAccessibleDescription(new RegExp(message.replace(/[.()]/g, '\\$&')));
    const id = (await input.getAttribute('aria-describedby')).split(' ').pop();
    await expect(page.locator('#' + id)).toHaveText(message);
    await expect(page.locator('#' + id)).toBeVisible();
    await expect(page.locator('#' + id)).toHaveAttribute('role', 'alert');
  };
  const clean = async (input) => {
    await expect(input).not.toHaveAttribute('aria-invalid', 'true');
    await expect(input).not.toHaveAttribute('aria-describedby', /port-error/);
  };

  // A Services row's Seestar port is refused, naming the row.
  await row.remote.fill('445');
  await shows(row.remote, 'Seestar port 445 is the File sharing for other apps (SMB) row above. Set its local port there.');
  await row.remote.fill('32323');
  await shows(row.remote, 'Seestar port 32323 is the Alpaca (NINA and other astronomy apps) row above. Set its local port there.');
  await row.remote.fill('5000');
  await clean(row.remote);
  // A local port is required.
  await row.local.fill('');
  await shows(row.local, 'Enter a local port from 1 to 65535.');
  // A local port another row uses: the error is on the row that duplicates.
  await row.local.fill('4445');
  await shows(row.local, 'Local port 4445 is already used by File sharing for other apps (SMB).');
  await clean(page.locator('#port-445'));
  await expect(page.locator('#ports-error')).toBeHidden();
  // Connect focuses the field in error and sends nothing.
  await page.locator('#start').click();
  await expect(row.local).toBeFocused();
  await row.local.fill('5001');
  await clean(row.local);
  expect(starts).toBe(0);

  // A refusal naming a port shows on that row as well as under the table,
  // and focuses the row's field.
  let refusal = { code: 'port_conflict', field: 'ports', service: 5000, local: 5001, message: 'Another program is using local port 5001.' };
  await page.route('**/api/start', (route) => route.fulfill({ status: 400, json: refusal }));
  await page.locator('#start').click();
  await expect(page.locator('#ports-error')).toHaveText(refusal.message);
  await shows(row.local, refusal.message);
  await expect(row.local).toBeFocused();
  await row.local.fill('5002');
  await clean(row.local);
  refusal = { code: 'port_conflict', field: 'ports', service: 445, local: 4445, message: 'Another program is using local port 4445.' };
  await page.locator('#start').click();
  await shows(page.locator('#port-445'), refusal.message);
  await expect(page.locator('#port-445')).toBeFocused();
  // A refusal of another field that names a port shows on its row as well.
  refusal = { code: 'bind', field: 'bind', service: 5000, local: 5002, message: 'Could not listen on this address.' };
  await page.locator('#start').click();
  await expect(page.locator('#bind-error')).toHaveText(refusal.message);
  await shows(row.local, refusal.message);
  await clean(page.locator('#port-445'));
});

test('a local port that follows its Seestar port is the duplicate it makes', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': { ...lan(), route: 'account', email: 'synthetic@example.invalid' } });
  await openApp(page, app.url);
  await remoteAdvanced(page);
  await field(page, 'Password').fill('synthetic-password');
  let starts = 0;
  page.on('request', (r) => { if (r.url().endsWith('/api/start')) starts++; });
  const add = page.getByRole('button', { name: 'Add a port', exact: true });
  // SMB is edited first; the added row's Local port then follows its
  // Seestar port onto the same number.
  await page.locator('#port-445').fill('5000');
  await add.click();
  await addedRow(page, 1).remote.fill('5000');
  await expect(addedRow(page, 1).local).toHaveValue('5000');
  await expect(addedRow(page, 1).local).toHaveAttribute('aria-invalid', 'true');
  await expect(addedRow(page, 1).local).toHaveAccessibleDescription(/Local port 5000 is already used by File sharing for other apps \(SMB\)\./);
  await expect(page.locator('#port-445')).not.toHaveAttribute('aria-invalid', 'true');
  await page.locator('#start').click();
  await expect(addedRow(page, 1).local).toBeFocused();
  // The same between added rows.
  await addedRow(page, 1).local.fill('6000');
  await add.click();
  await addedRow(page, 2).remote.fill('6000');
  await expect(addedRow(page, 2).local).toHaveAccessibleDescription(/Local port 6000 is already used by added port 1\./);
  await expect(addedRow(page, 1).local).not.toHaveAttribute('aria-invalid', 'true');
  await page.locator('#start').click();
  await expect(addedRow(page, 2).local).toBeFocused();
  expect(starts).toBe(0);
});

for (const off of [false, true]) {
  test(`the page forwards at most 128 ports${off ? ', counting a Services row that is off' : ''}`, async ({ page, startApp }) => {
    // 117 added ports and the 11 Services rows make 128.
    const ports = Array.from({ length: 117 }, (_, i) => ({ local: 30000 + i, device: 30000 + i }));
    const app = await startApp({ 'config.json': { ...lan(), route: 'account', email: 'synthetic@example.invalid', ports } });
    await openApp(page, app.url);
    await remoteAdvanced(page);
    await field(page, 'Password').fill('synthetic-password');
    await expect(addedRow(page, 117).remote).toHaveValue('30116');
    if (off) await page.locator('#port-80').fill('');
    let starts = 0;
    page.on('request', (r) => { if (r.url().endsWith('/api/start')) starts++; });
    await page.getByRole('button', { name: 'Add a port', exact: true }).click();
    const row = addedRow(page, 118);
    await row.remote.fill('30117');
    await page.locator('#start').click();
    await expect(row.remote).toHaveAttribute('aria-invalid', 'true');
    await expect(row.remote).toHaveAccessibleDescription(/At most 128 ports can be forwarded\./);
    await expect(row.remote).toBeFocused();
    expect(starts).toBe(0);
    await row.remove.click();
    for (const n of [1, 117]) await expect(addedRow(page, n).remote).not.toHaveAttribute('aria-invalid', 'true');
    await expect(page.locator('#added-ports [aria-invalid="true"], #ports [aria-invalid="true"]')).toHaveCount(0);
  });
}

for (const os of ['linux', 'darwin', 'windows']) {
  test(`a local port below 1024 shows the permissions guide by its row ${os === 'linux' ? 'on Linux' : 'not on ' + os}`, async ({ page, startApp }) => {
    if (os !== 'linux') {
      await page.route(/\/api\/status(\?first=1)?$/, async (route) => {
        const response = await route.fetch();
        route.fulfill({ response, json: { ...(await response.json()), os } });
      });
    }
    const app = await startApp({ 'config.json': { ...lan(), route: 'account', email: 'synthetic@example.invalid' } });
    await openApp(page, app.url);
    await remoteAdvanced(page);
    const guide = (input) => page.locator('#ports, #added-ports').locator('tr').filter({ has: input }).getByRole('link', { name: 'Port permissions guide' });
    const local80 = page.locator('#port-80');
    await local80.fill('80');
    if (os === 'linux') {
      await expect(guide(local80)).toBeVisible();
      await expect(local80).toHaveAccessibleDescription(/Linux needs permission for a port below 1024\. Port permissions guide/);
    } else await expect(guide(local80)).toBeHidden();
    await local80.fill('8080');
    await expect(guide(local80)).toBeHidden();
    await page.getByRole('button', { name: 'Add a port', exact: true }).click();
    const row = addedRow(page, 1);
    await row.remote.fill('800');
    if (os === 'linux') await expect(guide(row.local)).toBeVisible();
    else await expect(guide(row.local)).toBeHidden();
    await row.local.fill('8000');
    await expect(guide(row.local)).toBeHidden();
    await expect(page.locator('#port-permissions')).toBeHidden();
  });
}

for (const colorScheme of ['light', 'dark']) {
  test(`app screenshots in ${colorScheme}`, async ({ page, startApp }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    const shot = (name) => page.screenshot({
      path: path.join(__dirname, '..', 'screenshots', `${testInfo.project.name}-${colorScheme}-app-${name}.png`),
    });
    const fresh = await startApp();
    await openApp(page, fresh.url);
    await chooseLAN(page);
    await field(page, 'Telescope address').fill('a b');
    await page.getByRole('button', { name: 'Test' }).click();
    await expect(page.locator('#lan-address-error')).toBeVisible();
    await shot('first-run-error');

    const app = await startApp(setUp('/nowhere'));
    await openApp(page, app.url);
    await page.getByRole('button', { name: 'Test' }).click();
    await expect(lanResult(page)).toHaveText(connected);
    await shot('connection');
    await tab(page, 'Copy').click();
    await expect(page.locator('#c-title')).toHaveText('Copying is off.');
    await shot('copy-off');
    await tab(page, 'Browse').click();
    await expect(browseStatus(page)).toHaveText(rootStatus);
    await shot('browse');
    await tab(page, 'Copy').click();
    await page.getByRole('button', { name: 'Change…' }).click();
    await field(page, 'Leave out').fill('Video\n!Preview');
    await copySave(page).click();
    await expect(page.locator('#f-ex-err')).toBeVisible();
    await shot('copy-settings');
  });
}

// The layouts DESIGN asks the app to survive: twice the text size, the
// wider text spacing of WCAG 1.4.12, a 480 px wide window, and both of
// the first and last together.
const layouts = {
  'text-200': { width: 1280 },
  'text-spacing': { width: 1280 },
  'narrow-480': { width: 480 },
  'narrow-480-text-200': { width: 480 },
};

// restyle applies a layout's text changes to one document, through the
// CSSOM, which the pages' style policy allows. It doubles what is there,
// so it runs once per load.
async function restyle(target, layout) {
  await target.evaluate((layout) => {
    const all = [document.documentElement, ...document.querySelectorAll('*')];
    if (layout.endsWith('text-200')) {
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
// visible control is inside the window once scrolled to. It measures in
// one pass in the page: a round trip per control made the layouts slow.
async function fits(page) {
  const width = page.viewportSize().width;
  const [scrollWidth, clientWidth, boxes] = await page.evaluate(() => {
    const boxes = [];
    for (const el of document.querySelectorAll('button, input, select, textarea, iframe')) {
      // Visible as Playwright has it: a box, and not visibility: hidden.
      const shown = el.getBoundingClientRect();
      if (!shown.width || !shown.height || getComputedStyle(el).visibility === 'hidden') continue;
      el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      const box = el.getBoundingClientRect();
      boxes.push({ name: el.id || el.textContent, x: box.x, right: box.right });
    }
    return [document.documentElement.scrollWidth, document.documentElement.clientWidth, boxes];
  });
  expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
  expect(boxes.length).toBeGreaterThan(0);
  for (const { name, x, right } of boxes) {
    expect(x, name).toBeGreaterThanOrEqual(0);
    expect(right, name).toBeLessThanOrEqual(width + 0.5);
  }
}

// tabsWrap checks that the tabs wrap rather than overflow.
async function tabsWrap(page) {
  const [wrap, overflows] = await page.locator('#tabs').evaluate(
    (tabs) => [getComputedStyle(tabs).flexWrap, tabs.scrollWidth > tabs.clientWidth]);
  expect(wrap).toBe('wrap');
  expect(overflows).toBe(false);
}

// reopen reloads the page and opens a tab once its status is in.
async function reopen(page, name) {
  await page.reload();
  await expect(page.locator('#quit')).toBeEnabled();
  await tab(page, name).click();
}

for (const [layout, { width }] of Object.entries(layouts)) {
  test(`the app fits with ${layout}`, async ({ page, startApp }, testInfo) => {
    // Every tab and state, each restyled, measured control by control and
    // shot full page: about 30 s in WebKit, more than the default budget.
    test.slow();
    await page.setViewportSize({ width, height: 800 });
    const shot = (name) => page.screenshot({
      fullPage: true,
      path: path.join(__dirname, '..', 'screenshots', `${testInfo.project.name}-app-${layout}-${name}.png`),
    });

    const fresh = await startApp();
    await openApp(page, fresh.url);
    await chooseLAN(page);
    await restyle(page, layout);
    await fits(page);
    await tabsWrap(page);
    await shot('first-run');

    // The remote route's Advanced, with the Services table, which scrolls
    // in its own box.
    await page.getByLabel('Remotely through Telescope Network').check();
    await page.locator('#advanced summary').click();
    await expect(page.locator('#ports input').first()).toBeVisible();
    await fits(page);
    expect(await page.locator('#ports').evaluate((el) => getComputedStyle(el).overflowX)).toBe('auto');
    await shot('advanced');

    const first = await startApp({ 'config.json': lan() });
    await openCopy(page, first.url);
    await restyle(page, layout);
    await fits(page);
    await shot('copy-first-run');

    const app = await startApp(setUp('/nowhere'));
    await openCopy(page, app.url);
    await expect(page.locator('#c-title')).toHaveText('Copying is off.');
    await page.getByRole('button', { name: 'Change…' }).click();
    await page.getByText('Copy history', { exact: true }).click();
    await restyle(page, layout);
    await fits(page);
    await tabsWrap(page);
    await shot('copy');

    await tab(page, 'Browse').click();
    await expect(browseStatus(page)).toHaveText(rootStatus);
    const browse = await (await page.locator('#b-frame').elementHandle()).contentFrame();
    // The page has its layout from Copy; Browse's frame is its own document.
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

    for (const name of ['Free up space', 'Connection', 'Activity']) {
      await tab(page, name).click();
      if (name === 'Free up space') await page.locator('#x-always summary').click();
      await fits(page);
      await tabsWrap(page);
      await shot(name.toLowerCase().replaceAll(' ', '-'));
    }

    // An active pass, with long paths in flight and failed, and the lists
    // open. One route serves both tabs' views: WebKit can miss requests
    // after an unroute and a new route.
    let v = null, f = null;
    await fakeStatus(page, () => v, () => f);
    for (const [name, view] of Object.entries(copyViews)) {
      v = view;
      await reopen(page, 'Copy');
      await expect(page.locator('#c-title')).toHaveText(view.title);
      if (view.failed) await page.getByText('Show files').click();
      await restyle(page, layout);
      await fits(page);
      await shot('copy-' + name);
      if (view.activity) {
        await tab(page, 'Activity').click();
        await expect(page.locator('#events li[data-warn="true"]')).toHaveCount(1);
        await fits(page);
        await shot('activity-' + name);
      }
    }

    // Free up space's states, with a long path, its lists open and the
    // confirm dialog.
    v = null;
    for (const [name, view] of Object.entries(freeViews)) {
      f = view;
      await reopen(page, 'Free up space');
      await expect(page.locator('#x-title')).toHaveText(view.title);
      for (const s of await page.locator('#panel-free summary').filter({ visible: true }).all()) await s.click();
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
