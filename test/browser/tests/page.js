// What the specs share: the fixture's folders and the page's parts.
const { expect } = require('@playwright/test');

const m31 = 'MyWorks/M 31';
const folderURL = (p) => '/?path=' + encodeURIComponent(p);
const fileURL = (p) => '/file?path=' + encodeURIComponent(p);

const statusLine = (page) => page.locator('#status');
const currentCrumb = (page) => page.locator('#crumbs a[aria-current="page"]');
const viewer = (page) => page.locator('#viewer');
const link = (page, name) => page.getByRole('link', { name, exact: true });

// open goes to folder p and waits for its listing.
async function open(page, p, status) {
  await page.goto(p ? folderURL(p) : '/');
  await expect(statusLine(page)).toHaveText(status);
}

// loaded waits until img has loaded and returns its natural width.
async function loaded(img) {
  await expect.poll(() => img.evaluate((e) => (e.complete ? e.naturalWidth : 0))).toBeGreaterThan(0);
  return img.evaluate((e) => e.naturalWidth);
}

// headerText waits for the FITS header to be shown and returns it.
async function headerText(page) {
  const text = page.locator('#vtext');
  await expect.poll(() => text.textContent()).toMatch(/\nEND$/);
  return text.textContent();
}

module.exports = { m31, folderURL, fileURL, statusLine, currentCrumb, viewer, link, open, loaded, headerText };
