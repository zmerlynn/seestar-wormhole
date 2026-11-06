// The app's page: the first run on Connection and Copy, the address on
// this network, Copy settings and their field errors, the tabs and the
// Browse tab, against the page served without a window and the Samba
// fixture's browse tree.
// The Copy tab's passes are in copy.spec.js.
const path = require('node:path');
const fs = require('node:fs');
const {
  test, expect, share, refused, lan, readJSON, frame, browseStatus, tab, field, openApp, openCopy, fakeStatus, copyViews, freeViews,
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

const lanResult = (page) => page.locator('#lan-result');
const copySave = (page) => page.locator('#f-save');

// chooseLAN picks the route on this network on Connection.
async function chooseLAN(page) {
  await page.getByLabel('On this network').check();
  await expect(field(page, 'Telescope address')).toBeVisible();
}

test('the first run opens on Connection; saving an address opens Copy, whose first Save shows copying off', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  const connection = tab(page, 'Connection');
  await expect(connection).toHaveAttribute('aria-selected', 'true');
  for (const name of ['Copy', 'Browse', 'Free up space']) await expect(tab(page, name)).toBeDisabled();
  await expect(page.locator('#tabs-note')).toBeVisible();
  await expect(page.locator('#connection-text')).toHaveText('Not connected');
  await expect(page.getByLabel('Remotely through Telescope Network')).toBeChecked();
  // The arrow keys pass over the closed tabs.
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
  await expect(page.locator('#connection-text')).toHaveText('On this network: reachable');
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
  for (const box of await page.locator('#copy-form input, #copy-form textarea').all()) {
    expect(await box.getAttribute('placeholder')).toBeNull();
  }
  await expect(page.locator('#f-ex-help')).toContainText('For example: Video, /Preview.');
  await expect(page.locator('#f-cancel')).toBeHidden();
  // Browse says where to set the folder until it is saved.
  await tab(page, 'Browse').click();
  await expect(page.locator('#b-setup')).toBeVisible();
  await expect(page.locator('#b-frame')).toBeHidden();
  await page.getByRole('button', { name: 'Go to Copy' }).click();
  await expect(tab(page, 'Copy')).toHaveAttribute('aria-selected', 'true');
  await expect(field(page, 'Folder for your images')).toBeFocused();

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
    "Your Seestar's address on this network, with an optional port. Port 445 is the default. Copy, Browse and Free up space read the telescope's files at this address.");
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
  await expect(page.locator('#connection-text')).toHaveText('Not connected');
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
  await expect(page.locator('#connection-text')).toHaveText('On this network: reachable');
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
    await expect(page.locator('#connection-text')).toHaveText('On this network: reachable');
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
  const box = await page.locator('#lan-test').boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  // Copy's status is read once the page has rendered its own.
  await page.waitForResponse((r) => r.url().endsWith('/copy/api/status'));
  await page.mouse.up();
  await expect(lanResult(page)).toHaveText(connectedUnsaved);
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
    files_at_once: 4, older_than_days: 7, trust_state: false, free_excludes: [], free_includes: [],
    later: { kept: true },
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

test('Copy settings field errors show under each field and nothing is saved', async ({ page, startApp }) => {
  const files = setUp('/nowhere');
  const app = await startApp(files);
  await openCopy(page, app.url);
  await page.getByRole('button', { name: 'Change…' }).click();
  await page.locator('#f-int').fill('0');
  await field(page, 'Leave out').fill('Video\n\n[Pp]review');
  await field(page, 'Only copy').fill('MyWorks/');
  await copySave(page).click();

  await expect(page.locator('#f-int-err')).toHaveText('Enter a whole number of minutes from 1 to 1440.');
  await expect(page.locator('#f-ex-err')).toHaveText('Line 3: [ and \\ are not supported; use * and ?, and / between folders. Correct this pattern and save again.');
  await expect(page.locator('#f-in-err')).toHaveText('Line 1: omit the trailing /; a pattern matches files and folders. Correct this pattern and save again.');
  await expect(page.locator('#f-int')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#f-int')).toBeFocused();
  await expect(page.locator('#f-saved')).toHaveText('');
  expect(readJSON(app.copyFile)).toEqual(files['copy.json']);

  await page.locator('#f-int').fill('10');
  await field(page, 'Leave out').fill('Video\n\n*review');
  await copySave(page).click();
  await expect(field(page, 'Only copy')).toBeFocused();
  await expect(page.locator('#f-int-err')).toBeHidden();
  await expect(page.locator('#f-ex-err')).toBeHidden();
  await expect(page.locator('#f-int')).not.toHaveAttribute('aria-invalid', 'true');
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
    return Promise.all([fetch('/api/status'), fetch('/copy/api/status'), post('/copy/api/settings'), post('/api/route')]).then((rs) => rs.map((r) => r.status));
  });
  expect(codes).toEqual([403, 403, 403, 403]);
  await expect(page.locator('#notice')).toHaveText('Connection to the controls was lost. The proxy may still be running. Reopen the app to reconnect.');
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
  await expect(history).toHaveText(/^\/.*[/\\]seestar-copy[/\\]sync[/\\][0-9a-f]{64}\.jsonl$/);
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
    await field(page, 'Leave out').fill('Video\n[Pp]review');
    await copySave(page).click();
    await expect(page.locator('#f-ex-err')).toBeVisible();
    await shot('copy-settings');
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

    for (const name of ['Free up space', 'Connection', 'Activity']) {
      await tab(page, name).click();
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
