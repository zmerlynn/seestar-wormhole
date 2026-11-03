// The browse page against the Samba fixture's browse tree, made by
// internal/remote/testdata/samba/make-browse.sh.
const { test, expect } = require('@playwright/test');
const { m31, folderURL, fileURL, statusLine, currentCrumb, viewer, link, open, loaded, headerText } = require('./page');

const rootStatus = '2 folders, 2 files.';
const m31Status = '1 folder, 3 files.';

test('the root lists folders before files, with a count', async ({ page }) => {
  await open(page, '', rootStatus);
  const names = await page.locator('#list tbody td.name').allTextContents();
  expect(names.slice(0, 2).sort()).toEqual(['Long names/', 'MyWorks/']);
  expect(names.slice(2).sort()).toEqual(['00-notes.txt', 'empty.txt']);
});

test('the initial load does not move focus', async ({ page }) => {
  await open(page, m31, m31Status);
  await expect(currentCrumb(page)).toHaveText('M 31');
  await expect(currentCrumb(page)).not.toBeFocused();
});

test('folder links, breadcrumbs, Back and Forward navigate and focus the current breadcrumb', async ({ page }) => {
  await open(page, '', rootStatus);
  await expect(currentCrumb(page)).toHaveText('Share');

  await link(page, 'MyWorks/').click();
  await expect(page).toHaveURL(/\/\?path=MyWorks$/);
  await expect(statusLine(page)).toHaveText('1 folder, 0 files.');
  await expect(currentCrumb(page)).toHaveText('MyWorks');
  await expect(currentCrumb(page)).toBeFocused();

  await link(page, 'M 31/').click();
  await expect(page).toHaveURL(/\/\?path=MyWorks%2FM%2031$/);
  await expect(statusLine(page)).toHaveText(m31Status);
  await expect(currentCrumb(page)).toHaveText('M 31');
  await expect(currentCrumb(page)).toBeFocused();

  await page.locator('#crumbs').getByRole('link', { name: 'MyWorks', exact: true }).click();
  await expect(page).toHaveURL(/\/\?path=MyWorks$/);
  await expect(statusLine(page)).toHaveText('1 folder, 0 files.');
  await expect(currentCrumb(page)).toBeFocused();

  await page.goBack();
  await expect(page).toHaveURL(/\/\?path=MyWorks%2FM%2031$/);
  await expect(statusLine(page)).toHaveText(m31Status);
  await expect(currentCrumb(page)).toHaveText('M 31');
  await expect(currentCrumb(page)).toBeFocused();

  await page.goForward();
  await expect(page).toHaveURL(/\/\?path=MyWorks$/);
  await expect(currentCrumb(page)).toHaveText('MyWorks');
  await expect(currentCrumb(page)).toBeFocused();
  await expect(statusLine(page)).toHaveText('1 folder, 0 files.');
  await expect(link(page, 'M 31/')).toBeVisible();

  await page.locator('#crumbs').getByRole('link', { name: 'Share', exact: true }).click();
  await expect(statusLine(page)).toHaveText(rootStatus);
  await expect(currentCrumb(page)).toHaveText('Share');
  await expect(currentCrumb(page)).toBeFocused();
});

test('thumbnails render from the _thn.jpg previews', async ({ page }) => {
  await open(page, m31, m31Status);
  const thumbs = page.locator('#list td.thumb img');
  // The .fit, the .jpg and the _thn.jpg itself each show the 48-pixel preview.
  await expect(thumbs).toHaveCount(3);
  for (const img of await thumbs.all()) {
    await expect(img).toHaveAttribute('src', fileURL(m31 + '/Stacked_M 31_thn.jpg'));
    expect(await loaded(img)).toBe(48);
  }
});

test('clicking a name or a thumbnail opens the image viewer', async ({ page }) => {
  await open(page, m31, m31Status);
  const image = viewer(page).locator('img');

  await link(page, 'Stacked_M 31.jpg').click();
  await expect(viewer(page)).toBeVisible();
  await expect(page.locator('#vname')).toHaveText('Stacked_M 31.jpg');
  await expect(image).toHaveAttribute('alt', 'Image preview of Stacked_M 31.jpg');
  expect(await loaded(image)).toBe(160);
  await expect(image).toBeVisible();
  await expect(page.locator('#vnote')).toHaveText('');
  await expect(page.locator('#vtext')).toBeHidden();
  await page.locator('#vclose').click();
  await expect(viewer(page)).toBeHidden();

  const row = page.locator('#list tbody tr').filter({ has: link(page, 'Stacked_M 31.jpg') });
  await row.locator('td.thumb img').click();
  await expect(viewer(page)).toBeVisible();
  await expect(page.locator('#vname')).toHaveText('Stacked_M 31.jpg');
  expect(await loaded(image)).toBe(160);
  await expect(image).toBeVisible();
});

test('a .txt opens as text', async ({ page }) => {
  await open(page, '', rootStatus);
  await link(page, '00-notes.txt').click();
  await expect(viewer(page)).toBeVisible();
  await expect(page.locator('#vtext')).toHaveText('Seestar session notes\nM 31, 10 s subs, IR cut filter.');
  await expect(page.locator('#vnote')).toHaveText('');
  await expect(viewer(page).locator('img')).toBeHidden();
});

test('a .fit shows its header cards up to END', async ({ page }) => {
  await open(page, m31, m31Status);
  await link(page, 'Stacked_M 31.fit').click();
  await expect(viewer(page)).toBeVisible();
  const cards = (await headerText(page)).split('\n');
  expect(cards).toEqual([
    'SIMPLE  =                    T / conforms to FITS standard',
    'BITPIX  =                   16',
    'NAXIS   =                    2',
    'NAXIS1  =                    4',
    'NAXIS2  =                    4',
    "OBJECT  = 'M 31    '",
    'EXPTIME =                 10.0',
    'END',
  ]);
  await expect(page.locator('#vtext')).toBeVisible();
  await expect(page.locator('#vnote')).toHaveText('');
});

test('an empty file opens as empty text with no error', async ({ page }) => {
  await open(page, '', rootStatus);
  await link(page, 'empty.txt').click();
  await expect(viewer(page)).toBeVisible();
  // The note reads Loading… until the read ends, and an error would stay.
  await expect(page.locator('#vnote')).toHaveText('');
  const text = page.locator('#vtext');
  expect(await text.evaluate((e) => e.hidden)).toBe(false);
  expect(await text.textContent()).toBe('');
});

test('Save is a download link styled like Close', async ({ page }) => {
  await open(page, m31, m31Status);
  const name = m31 + '/Stacked_M 31.jpg';
  const href = fileURL(name) + '&download=1';
  await expect(page.getByRole('link', { name: 'Save Stacked_M 31.jpg', exact: true })).toHaveAttribute('href', href);

  await link(page, 'Stacked_M 31.jpg').click();
  const save = page.locator('#vsave');
  const close = page.locator('#vclose');
  expect(await save.evaluate((e) => e.tagName)).toBe('A');
  await expect(save).toHaveAttribute('download', '');
  await expect(save).toHaveAttribute('href', href);
  await expect(save).toHaveClass(/(^|\s)btn(\s|$)/);
  await expect(close).toHaveClass(/(^|\s)btn(\s|$)/);
  const look = (l) => l.evaluate((e) => {
    const s = getComputedStyle(e);
    return [s.borderTopWidth, s.borderTopStyle, s.borderTopColor, s.borderTopLeftRadius, s.backgroundColor, s.paddingTop, s.paddingLeft];
  });
  expect(await look(save)).toEqual(await look(close));

  const res = await page.request.get(href);
  expect(res.status()).toBe(200);
  expect(res.headers()['content-disposition']).toMatch(/^attachment;/);
});

test('Close, Escape and a backdrop click close the viewer; a click inside does not', async ({ page }) => {
  await open(page, '', rootStatus);
  const reopen = async () => {
    await link(page, '00-notes.txt').click();
    await expect(viewer(page)).toBeVisible();
    await expect(page.locator('#vtext')).toContainText('Seestar session notes');
  };

  await reopen();
  await page.locator('#vclose').click();
  await expect(viewer(page)).toBeHidden();

  await reopen();
  await page.keyboard.press('Escape');
  await expect(viewer(page)).toBeHidden();

  await reopen();
  await page.locator('#vtext').click();
  await page.locator('#vname').click();
  await expect(viewer(page)).toBeVisible();
  // The corner of the page is outside the small dialog: the backdrop.
  await page.mouse.click(5, 5);
  await expect(viewer(page)).toBeHidden();
});

test('a missing folder shows the error with how to retry', async ({ page }) => {
  await page.goto(folderURL('Missing'));
  await expect(statusLine(page)).toHaveText(
    'Not found on the device. Select Share or a parent folder above, or reload this page to retry.');
  await expect(page.locator('#list')).toBeHidden();
  await expect(currentCrumb(page)).toHaveText('Missing');
});

test('a long name does not widen the page', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 640 });
  await open(page, 'Long names', '0 folders, 1 file.');
  const [scrollWidth, clientWidth] = await page.evaluate(
    () => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
});

test('the favicon resolves', async ({ page }) => {
  await open(page, '', rootStatus);
  const href = await page.locator('link[rel="icon"]').getAttribute('href');
  const res = await page.request.get(new URL(href, page.url()).href);
  expect(res.status()).toBe(200);
  expect(res.headers()['content-type']).toBe('image/svg+xml');
});
