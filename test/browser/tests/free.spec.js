// The app's Free up space tab against the Samba fixture's browse tree,
// which is read-only: a preview, the confirm dialog and Cancel, and a
// settings save and a history clear that clear the preview. The confirmed delete is tested
// in the Samba job. What a check is looking at uses synthetic states.
const { test, expect, share, tab, field, openApp, fakeStatus } = require('./app');

const upToDate = /^Up to date\. Next check at \d\d:\d\d\.$/;
const safe = /^7 files \(\d.*\) are copied and safe to delete\.$/;

const button = (page, name) => page.getByRole('button', { name, exact: true });
const title = (page) => page.locator('#x-title');
const deleteButton = (page) => page.locator('#x-go');
const days = (page) => page.locator('#x-days');
const trust = (page) => page.getByLabel(/My copies have moved or the drive is offline/);

// copied starts an app that copies the browse tree, and waits for the
// pass, then opens Free up space.
async function copied(page, startApp) {
  const app = await startApp((folder) => ({ version: 1, address: share, folder, start_copying: true }));
  await openApp(page, app.url);
  await expect(page.locator('#c-title')).toHaveText(upToDate, { timeout: 30000 });
  await tab(page, 'Free up space').click();
  await expect(page.getByRole('tabpanel', { name: 'Free up space' })).toBeVisible();
  return app;
}

// find previews files of any age, and waits for the preview's title.
async function find(page, want = safe) {
  await days(page).fill('0');
  await button(page, 'Preview what to delete').click();
  await expect(title(page)).toHaveText(want, { timeout: 30000 });
}

test('a preview groups the files by folder, and the confirm dialog cancels back to Delete', async ({ page, startApp }) => {
  await copied(page, startApp);
  await expect(page.locator('#x-intro')).toHaveText(/^Free up space deletes files from the telescope only after/);
  await expect(page.locator('#x-restack')).toBeVisible();
  await expect(page.locator('#x-find-note')).toHaveText('Nothing is deleted until you confirm.');
  await find(page);
  // Preview went away: focus moves to the new state's heading, and the
  // status region says the stage.
  await expect(title(page)).toBeFocused();
  await expect(page.locator('#x-live')).toHaveText(safe);
  await expect(page.locator('#x-sub')).toHaveText('Any age · your copies checked');
  await expect(button(page, 'Preview what to delete')).toBeHidden();
  await expect(deleteButton(page)).toHaveText('Delete 7 files…');

  // Unchecking a folder narrows Delete's count.
  const rows = page.locator('#x-groups tr');
  expect(await rows.count()).toBeGreaterThan(1);
  const first = Number(await rows.first().locator('td.num').first().textContent());
  await rows.first().getByRole('checkbox').uncheck();
  const left = 7 - first;
  const files = left === 1 ? '1 file' : `${left} files`;
  await expect(deleteButton(page)).toHaveText(`Delete ${files}…`);
  await rows.first().locator('summary').click();
  await expect(rows.first().locator('li').first()).toBeVisible();

  await deleteButton(page).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('#x-ctitle')).toHaveText(`Delete ${files} from the telescope?`);
  await expect(dialog.locator('#x-cbody')).toHaveText(/^This frees \d.*\. It can't be undone: the telescope keeps no trash\. Each file is checked again just before it's deleted\.$/);
  await expect(button(page, 'Cancel')).toBeFocused();
  await button(page, 'Cancel').click();
  await expect(dialog).toBeHidden();
  await expect(deleteButton(page)).toBeFocused();

  // Escape closes it too.
  await deleteButton(page).click();
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(deleteButton(page)).toBeFocused();
  await expect(title(page)).toHaveText(safe);

  // Change options goes back to Start, with the options as found.
  await button(page, 'Change options').click();
  await expect(days(page)).toHaveValue('0');
  await expect(page.locator('#x-restack')).toBeHidden();
  await expect(page.locator('#x-intro')).toBeFocused();
});

test('a different preview closes an open confirm dialog', async ({ page, startApp }) => {
  // Another view's Find replaces the preview between two polls: the
  // status answers the same files under the next id.
  let replaced = false;
  await page.route('**/app/api/status', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    if (replaced && data.free.state === 'preview') data.free.id += 1;
    await route.fulfill({ response, json: data });
  });
  const deletes = [];
  page.on('request', (r) => {
    if (r.url().endsWith('/app/api/delete') && r.postDataJSON().action === 'delete') deletes.push(r.postDataJSON());
  });
  await copied(page, startApp);
  await find(page);
  await page.locator('#x-groups tr').first().getByRole('checkbox').uncheck();
  await deleteButton(page).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  replaced = true;
  await expect(dialog).toBeHidden();
  await expect(page.locator('#x-cgo')).toBeHidden();
  // The new preview's folders are all checked, and only a new confirm
  // can delete them.
  await expect(deleteButton(page)).toHaveText('Delete 7 files…');
  expect(deletes).toEqual([]);
});

test('a settings save that changes a pattern clears the preview', async ({ page, startApp }) => {
  await copied(page, startApp);
  await trust(page).check();
  await expect(page.locator('#x-intro')).toHaveText(/^Free up space uses seestar-copy's saved copy records\./);
  await find(page, /^7 files \(\d.*\) match the saved copy records\.$/);
  await expect(page.locator('#x-sub')).toHaveText('Any age · Your copies were not checked.');

  await tab(page, 'Settings').click();
  await page.locator('#s-stop').click();
  await field(page, 'Leave out').fill('Notes');
  await button(page, 'Save').click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await tab(page, 'Free up space').click();
  await expect(page.locator('#x-changed')).toHaveText('Settings or copy history changed. Preview again before deleting.');
  await expect(button(page, 'Preview what to delete')).toBeVisible();
  await expect(deleteButton(page)).toBeHidden();
  await expect(trust(page)).toBeChecked();
});

test('Clear history clears the preview', async ({ page, startApp }) => {
  await copied(page, startApp);
  await find(page);
  await tab(page, 'Settings').click();
  await page.locator('#s-stop').click();
  await button(page, 'Clear history…').click();
  await button(page, 'Clear history').click();
  await expect(page.locator('#h-result')).toHaveText('Copy history cleared.');
  await tab(page, 'Free up space').click();
  await expect(page.locator('#x-changed')).toHaveText('Settings or copy history changed. Preview again before deleting.');
  await expect(button(page, 'Preview what to delete')).toBeVisible();
  await expect(deleteButton(page)).toBeHidden();
  // With no history, nothing is safe to delete.
  await find(page, /^Nothing to delete yet\./);
});

test('a bad number of days is refused at its field', async ({ page, startApp }) => {
  await copied(page, startApp);
  await days(page).fill('-1');
  await button(page, 'Preview what to delete').click();
  await expect(page.locator('#x-days-err')).toHaveText('Enter a whole number of days, 0 or more.');
  await expect(days(page)).toHaveAttribute('aria-invalid', 'true');
  await expect(days(page)).toBeFocused();
});

test('a check names the folder or file it is looking at, and announces only its stages', async ({ page, startApp }) => {
  const app = await startApp((folder) => ({ version: 1, address: share, folder, start_copying: false }));
  let f = null;
  await fakeStatus(page, () => null, () => f);
  await openApp(page, app.url);
  await tab(page, 'Free up space').click();
  const file = page.locator('#x-file');
  const live = page.locator('#x-live');
  await expect(file).toBeHidden();
  // The line is outside the tab's one status region.
  await expect(page.locator('#p-free [role=status]')).toHaveCount(1);

  f = { state: 'checking', title: 'Checking files…', file: 'Listing MyWorks… (1 folder)' };
  await expect(file).toHaveText('Listing MyWorks… (1 folder)');
  await expect(page.locator('#x-busy')).toBeVisible();
  await expect(live).toHaveText('Checking files…');
  f = { state: 'checking', title: 'Checking files…', file: 'Listing MyWorks/M 31… (2 folders)' };
  await expect(file).toHaveText('Listing MyWorks/M 31… (2 folders)');
  // It keeps one line, with the whole path in its title.
  await expect(file).toHaveAttribute('title', 'Listing MyWorks/M 31… (2 folders)');
  await expect(file).toHaveCSS('white-space', 'nowrap');
  await expect(file).toHaveCSS('text-overflow', 'ellipsis');
  f = {
    state: 'checking', title: 'Checking 3 files…', progress: { done: 0, total: 9, text: 'Checking your copies: 0 B of 9 B' },
    file: 'Checking MyWorks/M 31/a.fit…',
  };
  await expect(file).toHaveText('Checking MyWorks/M 31/a.fit…');
  await expect(page.locator('#x-hash')).toBeVisible();
  await expect(page.locator('#x-busy')).toBeHidden();
  await expect(live).toHaveText('Checking files…');

  f = { state: 'deleting', title: 'Deleting 1 of 2…', progress: { done: 0, total: 2 }, file: 'Checking MyWorks/M 31/a.fit…' };
  await expect(live).toHaveText('Deleting 1 of 2…');
  await expect(file).toHaveText('Checking MyWorks/M 31/a.fit…');
  // Stop stays put while the line changes, even when it is empty.
  const stop = button(page, 'Stop');
  const y = async () => (await stop.boundingBox()).y;
  const at = await y();
  f = { state: 'deleting', title: 'Deleting 1 of 2…', progress: { done: 0, total: 2 } };
  await expect(file).toHaveText('');
  await expect(file).toBeVisible();
  expect(await y()).toBe(at);
  f = { state: 'deleting', title: 'Deleting 2 of 2…', progress: { done: 1, total: 2 }, file: 'Last deleted: MyWorks/M 31/a.fit' };
  await expect(file).toHaveText('Last deleted: MyWorks/M 31/a.fit');
  expect(await y()).toBe(at);
  await expect(live).toHaveText('Deleting 1 of 2…');
  // A finished run clears and hides the line.
  f = { state: 'done', title: 'Deleted 2 files' };
  await expect(file).toBeHidden();
  await expect(file).toHaveText('');
  await expect(file).toHaveAttribute('title', '');
});
