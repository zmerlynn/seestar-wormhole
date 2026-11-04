// Screenshots of the listing and the viewer in light and dark, for review.
// CI uploads the screenshots folder; nothing compares them.
const path = require('node:path');
const { test, expect } = require('@playwright/test');
const { m31, viewer, link, open, loaded, headerText } = require('./page');

for (const colorScheme of ['light', 'dark']) {
  test(`screenshots in ${colorScheme}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    const shot = (name) => page.screenshot({
      path: path.join(__dirname, '..', 'screenshots', `${testInfo.project.name}-${colorScheme}-${name}.png`),
    });
    const close = async () => {
      await page.locator('#vclose').click();
      await expect(viewer(page)).toBeHidden();
    };

    await open(page, m31, '1 folder, 3 files.');
    for (const img of await page.locator('#list td.thumb img').all()) await loaded(img);
    await shot('listing');

    await link(page, 'Stacked_M 31.jpg').click();
    await loaded(viewer(page).locator('img'));
    await expect(page.locator('#vnote')).toHaveText('');
    await shot('viewer-image');
    await close();

    await link(page, 'Stacked_M 31.fit').click();
    await headerText(page);
    await expect(page.locator('#vnote')).toHaveText('');
    await shot('viewer-fits');
    await close();

    await open(page, '', '2 folders, 2 files.');
    await link(page, '00-notes.txt').click();
    await expect(page.locator('#vtext')).toContainText('Seestar session notes');
    await shot('viewer-text');
  });
}
