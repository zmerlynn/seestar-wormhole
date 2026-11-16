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
