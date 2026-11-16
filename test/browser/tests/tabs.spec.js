// The shell's tabs, against the app served without a window: its
// Default profile is the app's state folder, opened first.
const fs = require('node:fs');
const path = require('node:path');
const { test, expect, lan } = require('./app');

// addGarden makes a named profile beside the app's Default: a private
// folder of its own, as New profile makes it.
function addGarden(app) {
  for (const dir of [path.join(app.state, 'profiles'), path.join(app.state, 'profiles', 'Garden')]) {
    fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
    fs.chmodSync(dir, 0o700);
  }
}
const menu = (page) => page.locator('#a-dialog');
const row = (page, name) => page.locator('#a-list button', { hasText: name });
const ptab = (page, name) => page.locator('#profile-tabs').getByRole('tab', { name });
const frameOf = (page, n) => page.frameLocator('#pframe-' + n);

// openShell loads the shell and waits for Default's page in its tab.
async function openShell(page, app) {
  await page.goto(app.shell);
  await expect(frameOf(page, 1).locator('#quit')).toBeEnabled();
}

test('Default alone has the tab bar and +', async ({ page, startApp }) => {
  const app = await startApp();
  await openShell(page, app);
  await expect(page).toHaveTitle('seestar-wormhole');
  await expect(page.getByRole('tablist', { name: 'Profiles' }).getByRole('tab')).toHaveCount(1);
  await expect(ptab(page, 'Default')).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('button', { name: 'Open a profile' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Close Default' })).toBeVisible();
});

test('a second tab opens from +, and switching keeps each page as it was', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openShell(page, app);
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await expect(menu(page)).toBeVisible();
  await expect(page.locator('#a-list button')).toHaveText(['Garden', 'New profile…']);
  await expect(row(page, 'Garden')).toBeFocused();
  await row(page, 'Garden').click();
  await expect(menu(page)).toBeHidden();
  await expect(ptab(page, 'Garden')).toHaveAttribute('aria-selected', 'true');
  await expect(frameOf(page, 2).locator('#quit')).toBeEnabled();
  expect(fs.existsSync(path.join(app.state, 'profiles', 'Garden', 'ui-runtime.json'))).toBe(true);

  // An unsaved edit in Default survives switching away and back, by
  // click and by Ctrl+Tab from inside a page.
  await ptab(page, 'Default').click();
  const email = frameOf(page, 1).locator('#email');
  await email.fill('garden-edit@example.invalid');
  await email.press('Control+Tab');
  await expect(ptab(page, 'Garden')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#pframe-1')).toBeHidden();
  await page.keyboard.press('Control+Shift+Tab');
  await expect(ptab(page, 'Default')).toHaveAttribute('aria-selected', 'true');
  await expect(email).toHaveValue('garden-edit@example.invalid');

  // Escape closes the menu, and focus goes back to +.
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await expect(page.locator('#a-list button')).toHaveText(['New profile…']);
  await page.keyboard.press('Escape');
  await expect(menu(page)).toBeHidden();
  await expect(page.getByRole('button', { name: 'Open a profile' })).toBeFocused();
});

test('Ctrl+Tab switches tabs from inside Browse, both ways', async ({ page, startApp }) => {
  const app = await startApp({ 'config.json': lan() });
  addGarden(app);
  await openShell(page, app);
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await row(page, 'Garden').click();
  await expect(frameOf(page, 2).locator('#quit')).toBeEnabled();
  await ptab(page, 'Default').click();
  await frameOf(page, 1).getByRole('tab', { name: 'Browse', exact: true }).click();
  const crumb = frameOf(page, 1).frameLocator('#b-frame').locator('#crumbs a').first();
  await crumb.focus();
  await crumb.press('Control+Tab');
  await expect(ptab(page, 'Garden')).toHaveAttribute('aria-selected', 'true');
  await ptab(page, 'Default').click();
  await crumb.focus();
  await crumb.press('Control+Shift+Tab');
  await expect(ptab(page, 'Garden')).toHaveAttribute('aria-selected', 'true');
});

// The selected tab's page takes focus as it loads and as tabs switch from
// a page, unless something in the shell has focus.
test('the selected page takes focus, unless the shell has it', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openShell(page, app);
  await expect(frameOf(page, 1).locator('#start')).toBeFocused();
  // A profile opened from + leaves focus on its tab while its page loads.
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await row(page, 'Garden').click();
  await expect(frameOf(page, 2).locator('#quit')).toBeEnabled();
  await expect(ptab(page, 'Garden')).toBeFocused();
  // Ctrl+Tab from the shell stays in the shell, on the tab.
  await page.keyboard.press('Control+Tab');
  await expect(ptab(page, 'Default')).toBeFocused();
  // From a page, it lands in the next one, whose Start takes focus as
  // its page first does, and back.
  await frameOf(page, 1).locator('#start').focus();
  await page.keyboard.press('Control+Tab');
  await expect(ptab(page, 'Garden')).toHaveAttribute('aria-selected', 'true');
  await expect(frameOf(page, 2).locator('#start')).toBeFocused();
  await page.keyboard.press('Control+Tab');
  await expect(ptab(page, 'Default')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#pframe-1')).toBeFocused();
});

// The native window's shell can finish loading before its first tab's page
// does, as on Linux; the page still reads its status and takes focus.
test('a page that loads after the shell still takes focus', async ({ page, startApp }) => {
  const app = await startApp();
  let release;
  const held = new Promise((r) => { release = r; });
  await page.route('**/p/1/', async (route) => { await held; await route.continue(); });
  await page.goto(app.shell);
  await expect(page.locator('#pframe-1')).toBeAttached();
  release();
  await expect(frameOf(page, 1).locator('#status')).toHaveText('Not connected');
  await expect(frameOf(page, 1).locator('#start')).toBeFocused();
});

// A native window not yet focused as its first tab's page loads, as
// WKWebView's can be at launch, gives neither the shell nor the page a
// focus event; Connect takes focus anyway, ready for when the window
// gets it.
test('a page that loads in an unfocused window still takes focus', async ({ page, startApp }) => {
  const app = await startApp();
  await page.addInitScript(() => {
    Document.prototype.hasFocus = () => false;
    window.addEventListener('focus', (event) => event.stopImmediatePropagation(), true);
  });
  await page.goto(app.shell);
  await expect(frameOf(page, 1).locator('#status')).toHaveText('Not connected');
  await expect(frameOf(page, 1).locator('#start')).toBeFocused();
});

// WKWebView focuses the shell's first tab itself as its window first takes
// focus, which is no user's choice: the page takes focus anyway.
test('a page takes focus from the tab the web view focused at launch', async ({ page, startApp }) => {
  const app = await startApp();
  let release;
  const held = new Promise((r) => { release = r; });
  await page.route('**/p/1/', async (route) => { await held; await route.continue(); });
  await page.goto(app.shell);
  await expect(page.locator('#pframe-1')).toBeAttached();
  await page.evaluate(() => document.getElementById('ptab-1').focus());
  release();
  await expect(frameOf(page, 1).locator('#status')).toHaveText('Not connected');
  await expect(frameOf(page, 1).locator('#start')).toBeFocused();
});

// Once the page has had focus, a tab focused without a key or click, as
// a screen reader focuses it, keeps focus through polls.
test('a tab focused after launch keeps focus', async ({ page, startApp }) => {
  const app = await startApp();
  await openShell(page, app);
  await expect(frameOf(page, 1).locator('#start')).toBeFocused();
  await page.evaluate(() => document.getElementById('ptab-1').focus());
  await page.waitForTimeout(1200);
  await expect(ptab(page, 'Default')).toBeFocused();
});

// The window's close while a saved copy runs asks before any key or
// click in the window, and its dialog keeps focus through polls.
test('the quit dialog keeps focus before the user acts', async ({ page, startApp }) => {
  const app = await startApp();
  await openShell(page, app);
  await expect(frameOf(page, 1).locator('#start')).toBeFocused();
  // Default answers as copying, without a telescope to copy from.
  await page.route('**/api/tabs', async (route) => {
    const response = await route.fetch();
    const v = await response.json();
    v.tabs[0].copying = true;
    await route.fulfill({ response, json: v });
  });
  await page.evaluate(() => window.confirmQuit());
  await expect(page.locator('#k-confirm')).toBeVisible();
  await page.waitForTimeout(1200);
  expect(await page.evaluate(() => document.getElementById('k-confirm').contains(document.activeElement))).toBe(true);
});

test('New profile shows a refused name under the field and opens what it creates', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openShell(page, app);
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await row(page, 'New profile…').click();
  const name = page.getByLabel('Name', { exact: true });
  await expect(name).toBeFocused();
  await name.fill('garden ');
  await page.locator('#a-create').click();
  await expect(page.locator('#a-name-err')).toHaveText("There's already a profile named Garden.");
  await expect(name).toHaveAttribute('aria-invalid', 'true');
  await name.fill('  Backyard S30 ');
  await expect(page.locator('#a-name-err')).toBeHidden();
  await page.keyboard.press('Enter');
  await expect(menu(page)).toBeHidden();
  await expect(ptab(page, 'Backyard S30')).toHaveAttribute('aria-selected', 'true');
  expect(fs.statSync(path.join(app.state, 'profiles', 'Backyard S30')).isDirectory()).toBe(true);
});

test('closing a tab releases its profile, and closing the last quits the app', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openShell(page, app);
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await row(page, 'Garden').click();
  await expect(frameOf(page, 2).locator('#quit')).toBeEnabled();

  // Neither is connected, so neither asks.
  await page.getByRole('button', { name: 'Close Garden' }).click();
  await expect(page.locator('#profile-tabs').getByRole('tab')).toHaveCount(1);
  await expect(ptab(page, 'Default')).toHaveAttribute('aria-selected', 'true');
  await expect.poll(() => fs.existsSync(path.join(app.state, 'profiles', 'Garden', 'ui-runtime.json'))).toBe(false);
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await expect(page.locator('#a-list button')).toHaveText(['Garden', 'New profile…']);
  await page.keyboard.press('Escape');

  await page.getByRole('button', { name: 'Close Default' }).click();
  await expect(page.locator('#ended')).toHaveText('seestar-wormhole has quit. You can close this browser tab.');
  await expect(page.locator('#tabbar')).toBeHidden();
  expect(await app.exited).toBe(0);
});

test('closing an unselected tab by its button leaves focus on the selected tab', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openShell(page, app);
  await page.getByRole('button', { name: 'Open a profile' }).click();
  await row(page, 'Garden').click();
  await expect(frameOf(page, 2).locator('#quit')).toBeEnabled();
  await page.getByRole('button', { name: 'Close Default' }).click();
  await expect(page.locator('#profile-tabs').getByRole('tab')).toHaveCount(1);
  await expect(ptab(page, 'Garden')).toBeFocused();
});

test('a profile opened while the last close asks keeps the app and shows it', async ({ page, startApp }) => {
  const app = await startApp();
  await openShell(page, app);
  // Default answers as connected, without a telescope to connect to.
  await page.route('**/api/tabs', async (route) => {
    const response = await route.fetch();
    const v = await response.json();
    for (const t of v.tabs) t.active = true;
    await route.fulfill({ response, json: v });
  });
  await page.getByRole('button', { name: 'Close Default' }).click();
  await expect(page.locator('#k-confirm')).toBeVisible();
  await page.unroute('**/api/tabs');
  await expect(page.locator('#k-cgo')).toHaveText('Close and quit');

  // A launch of Garden asks the app to open it meanwhile.
  addGarden(app);
  const status = await page.evaluate(async () => (await fetch('api/activate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Seestar-Token': sessionStorage.getItem('seestar-control-token') },
    body: JSON.stringify({ profile: 'Garden' }),
  })).status);
  expect(status).toBe(200);
  await page.locator('#k-cgo').click();
  await expect(page.locator('#profile-tabs').getByRole('tab')).toHaveCount(1);
  await expect(ptab(page, 'Garden')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#ended')).toBeHidden();
  await expect(frameOf(page, 2).locator('#quit')).toBeEnabled();
});
