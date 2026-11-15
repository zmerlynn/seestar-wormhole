// The Profiles dialog, against the page served without a window as the
// Default profile. A profile it opens is only printed to the app's log.
const fs = require('node:fs');
const path = require('node:path');
const { test, expect, openApp } = require('./app');

// addGarden makes a named profile beside the app's Default: a private
// folder of its own, as New profile makes it.
function addGarden(app) {
  for (const dir of [path.join(app.state, 'profiles'), path.join(app.state, 'profiles', 'Garden')]) {
    fs.mkdirSync(dir, { mode: 0o700 });
    fs.chmodSync(dir, 0o700);
  }
}
const dialog = (page) => page.locator('#p-dialog');
const row = (page, name) => page.locator('#p-list button', { hasText: name });

test('Default alone is not named, and Profiles… is there', async ({ page, startApp }) => {
  const app = await startApp();
  await openApp(page, app.url);
  await expect(page.locator('#profiles-open')).toBeVisible();
  await expect(page.locator('#profile-name')).toBeHidden();
  await expect(page).toHaveTitle('seestar-wormhole');
});

test('Profiles lists this window and the others, and opens one', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openApp(page, app.url);
  await expect(page.locator('#profile-name')).toHaveText('· Default');
  await expect(page).toHaveTitle('seestar-wormhole — Default');

  await page.locator('#profiles-open').click();
  await expect(dialog(page)).toBeVisible();
  await expect(page.locator('#p-cancel')).toBeFocused();
  await expect(page.locator('#p-show')).toBeHidden();
  await expect(page.locator('#p-list button')).toHaveText(['Default — this window', 'Garden']);
  await row(page, 'Garden').click();
  await expect(dialog(page)).toBeHidden();
  await expect.poll(() => app.log).toContain('open profile Garden');

  // Escape closes it, and focus goes back to Profiles….
  await page.locator('#profiles-open').click();
  await expect(page.locator('#p-cancel')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog(page)).toBeHidden();
  await expect(page.locator('#profiles-open')).toBeFocused();
});

test('New profile shows a refused name under the field and opens what it creates', async ({ page, startApp }) => {
  const app = await startApp();
  addGarden(app);
  await openApp(page, app.url);
  await page.locator('#profiles-open').click();
  await page.locator('#p-new').click();
  const name = page.getByLabel('Name', { exact: true });
  await expect(name).toBeFocused();
  await name.fill('garden ');
  await page.locator('#p-create').click();
  await expect(page.locator('#p-name-err')).toHaveText("There's already a profile named Garden.");
  await expect(name).toHaveAttribute('aria-invalid', 'true');
  await name.fill('  Backyard S30 ');
  await expect(page.locator('#p-name-err')).toBeHidden();
  await page.keyboard.press('Enter');
  await expect(dialog(page)).toBeHidden();
  await expect.poll(() => app.log).toContain('open profile Backyard S30');
  expect(fs.statSync(path.join(app.state, 'profiles', 'Backyard S30')).isDirectory()).toBe(true);
});
