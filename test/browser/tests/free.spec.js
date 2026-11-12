// The app's Free up space tab against the Samba fixture's browse tree,
// which is read-only: a preview, the confirm dialog and Cancel, the tab's
// own patterns, and what a settings save or a history clear does to the
// preview. The confirmed delete is tested in the Samba job. What a check is
// looking at uses synthetic states.
const fs = require('fs');
const { test, expect, lan, tab, field, openCopy, fakeStatus, readJSON } = require('./app');

const upToDate = /^Up to date\. Next check at \d\d:\d\d\.$/;
const safe = /^7 files \(\d.*\) are copied and safe to delete\.$/;

const button = (page, name) => page.getByRole('button', { name, exact: true });
const title = (page) => page.locator('#x-title');
const deleteButton = (page) => page.locator('#x-go');
const days = (page) => page.locator('#x-days');
const proof = (page) => page.getByLabel('Before deleting, check');

// copying is a saved route to the fixture and copy settings that copy to
// the app's folder.
const copying = (start) => ({
  'config.json': lan(), 'copy.json': (folder) => ({ version: 1, folder, start_copying: start }),
});

// copied starts an app that copies the browse tree, and waits for the
// pass, then opens Free up space.
async function copied(page, startApp) {
  const app = await startApp(copying(true));
  await openCopy(page, app.url);
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
  await expect(page.locator('#free-chip')).toBeHidden();
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
  await page.route('**/copy/api/status', async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    if (replaced && data.free.state === 'preview') data.free.id += 1;
    await route.fulfill({ response, json: data });
  });
  const deletes = [];
  page.on('request', (r) => {
    if (r.url().endsWith('/copy/api/delete') && r.postDataJSON().action === 'delete') deletes.push(r.postDataJSON());
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

test("a settings save keeps the preview unless it changes the folder: Copy's patterns are not Free up space's", async ({ page, startApp }) => {
  const app = await copied(page, startApp);
  await proof(page).selectOption('Only the copy history (copies moved or drive offline)');
  await expect(page.locator('#x-intro')).toHaveText("Free up space uses this app's saved copy records. Your copies will not be checked.");
  await find(page, /^7 files \(\d.*\) match the saved copy records\.$/);
  await expect(page.locator('#x-sub')).toHaveText('Any age · Your copies were not checked.');

  await tab(page, 'Copy').click();
  await button(page, 'Change…').click();
  await page.locator('#s-stop').click();
  await expect(field(page, 'Folder for your images')).toBeFocused();
  await field(page, 'Leave out').fill('MyWorks');
  await button(page, 'Save').click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await tab(page, 'Free up space').click();
  await expect(page.locator('#x-changed')).toBeHidden();
  await expect(deleteButton(page)).toHaveText('Delete 7 files…');

  await tab(page, 'Copy').click();
  await button(page, 'Change…').click();
  await field(page, 'Folder for your images').fill(app.folder + '-other');
  await button(page, 'Save').click();
  await expect(page.locator('#f-saved')).toHaveText('Settings saved.');
  await tab(page, 'Free up space').click();
  await expect(page.locator('#x-changed')).toHaveText('Settings or copy history changed. Preview again before deleting.');
  await expect(button(page, 'Preview what to delete')).toBeVisible();
  await expect(deleteButton(page)).toBeHidden();
  await expect(proof(page)).toHaveValue('state');
});

test('Before deleting, check saves each choice, and Nothing previews and deletes files never copied', async ({ page, startApp }) => {
  // Copying never starts: no file on the telescope has been copied, and
  // the folder is empty.
  const app = await startApp(copying(false));
  fs.mkdirSync(app.folder, { recursive: true });
  await openCopy(page, app.url);
  await tab(page, 'Free up space').click();
  const warn = page.locator('#x-none-warn');
  await expect(proof(page)).toHaveValue('hash');
  await expect(warn).toBeHidden();
  await find(page, /^Nothing to delete yet\./);

  await proof(page).selectOption('Nothing: delete what\'s selected, copied or not');
  await expect(warn).toHaveText("Files you haven't copied are deleted too, and can't be recovered.");
  await expect(warn).toBeVisible();
  await expect(page.locator('#x-folder-text')).toHaveText("Your copies aren't checked.");
  await expect(page.locator('#x-intro')).toHaveText("Free up space deletes what's selected from the telescope, whether or not it was copied.");
  await find(page, /^7 files \(\d.*\) are selected to delete\.$/);
  await expect(page.locator('#x-sub')).toHaveText("Any age · Copies not checked: files you haven't copied are deleted too.");
  expect(readJSON(app.copyFile)).toMatchObject({ trust_state: false, ignore_state: true });

  // The confirm says the files weren't checked; the fixture is read-only,
  // so the delete it sends is refused there.
  const deletes = [];
  page.on('request', (r) => {
    if (r.url().endsWith('/copy/api/delete') && r.postDataJSON().action === 'delete') deletes.push(r.postDataJSON());
  });
  await page.locator('#x-groups tr').first().getByRole('checkbox').uncheck();
  await deleteButton(page).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.locator('#x-cbody')).toHaveText(/ These files weren't checked against your copies\. Any you haven't copied are gone for good\.$/);
  await page.locator('#x-cgo').click();
  await expect(dialog).toBeHidden();
  await expect(page.locator('#x-done')).toBeVisible({ timeout: 30000 });
  expect(deletes.length).toBe(1);

  // A reload shows the choice saved, and its warning.
  await page.reload();
  await expect(page.locator('#quit')).toBeEnabled();
  await tab(page, 'Free up space').click();
  await page.locator('#x-done').click();
  await expect(proof(page)).toHaveValue('none');
  await expect(warn).toBeVisible();

  for (const [label, value, want] of [
    ['Only the copy history (copies moved or drive offline)', 'state', { trust_state: true, ignore_state: false }],
    ['That my copy matches', 'hash', { trust_state: false, ignore_state: false }],
  ]) {
    await proof(page).selectOption(label);
    await expect(warn).toBeHidden();
    await find(page, /^Nothing to delete yet\./);
    // The title may still be the last preview's, shown before the reload,
    // so wait for the save rather than read the file once.
    await expect.poll(() => readJSON(app.copyFile)).toMatchObject(want);
    await page.reload();
    await expect(page.locator('#quit')).toBeEnabled();
    await tab(page, 'Free up space').click();
    await expect(proof(page)).toHaveValue(value);
  }
});

test('Keep on the telescope and Only free up select what a preview deletes, and are saved', async ({ page, startApp }) => {
  const app = await copied(page, startApp);
  await expect(page.locator('#x-patterns-help')).toHaveText(
    "These patterns are separate from Copy's Leave out and Only copy, which don't change what is deleted.");
  // Always kept sits with the fields, closed, and opens to the
  // telescope's built-in patterns, each named in plain words.
  const always = page.locator('#x-always');
  await expect(always.locator('summary')).toHaveText('Always kept');
  await expect(always).not.toHaveAttribute('open');
  await always.locator('summary').click();
  await expect(always.locator('li')).toHaveText([
    "The telescope's log and scratch folders: /log, /tmp_stitch, /batch_stack_tmp",
    'Names starting with a dot: .*',
    'Unfinished .tmp files: *.tmp',
    'Windows system files: Thumbs.db, desktop.ini, $RECYCLE.BIN, System Volume Information',
  ]);
  await expect(always.locator('li').first()).toBeVisible();
  const folders = () => page.locator('#x-groups summary').allTextContents();
  await find(page);
  const all = await folders();
  expect(all).toContain('MyWorks/M 31');
  // The pattern matches the folder and everything below it.
  const inM31 = (f) => f === 'MyWorks/M 31' || f.startsWith('MyWorks/M 31/');
  expect(all.filter((f) => !inM31(f)).length).toBeGreaterThan(0);

  await button(page, 'Change options').click();
  await field(page, 'Keep on the telescope').fill('/MyWorks/M 31');
  await find(page, /^\d+ files? \(\d.*\) (is|are) copied and safe to delete\.$/);
  await expect(title(page)).not.toHaveText(safe);
  expect(await folders()).toEqual(all.filter((f) => !inM31(f)));

  await button(page, 'Change options').click();
  await field(page, 'Keep on the telescope').fill('');
  await field(page, 'Only free up').fill('\n/MyWorks/M 31');
  await find(page, /^\d+ files? \(\d.*\) (is|are) copied and safe to delete\.$/);
  expect(await folders()).toEqual(all.filter(inM31));
  expect(readJSON(app.copyFile)).toMatchObject({ excludes: [], free_excludes: [], free_includes: ['/MyWorks/M 31'] });

  // A reload shows what was saved.
  await page.reload();
  await expect(page.locator('#quit')).toBeEnabled();
  await tab(page, 'Free up space').click();
  await button(page, 'Change options').click();
  await expect(field(page, 'Only free up')).toHaveValue('/MyWorks/M 31');
  await expect(field(page, 'Keep on the telescope')).toHaveValue('');
});

test('a bad pattern is refused at its field', async ({ page, startApp }) => {
  await copied(page, startApp);
  await field(page, 'Keep on the telescope').fill('Video\n[x');
  await button(page, 'Preview what to delete').click();
  const err = page.locator('#x-ex-err');
  await expect(err).toHaveText(/^Line 2: .+\. Correct this pattern and preview again\.$/);
  await expect(field(page, 'Keep on the telescope')).toHaveAttribute('aria-invalid', 'true');
  await expect(field(page, 'Keep on the telescope')).toBeFocused();
  await expect(page.locator('#x-in-err')).toBeHidden();
  await expect(title(page)).toHaveText('');
  // Correcting it clears the message.
  await field(page, 'Keep on the telescope').fill('Video');
  await find(page);
  await expect(err).toBeHidden();
  await expect(field(page, 'Keep on the telescope')).not.toHaveAttribute('aria-invalid', 'true');
});

test('Clear history clears the preview', async ({ page, startApp }) => {
  await copied(page, startApp);
  await find(page);
  await tab(page, 'Copy').click();
  await button(page, 'Stop copying').click();
  await expect(page.locator('#c-title')).toHaveText('Copying is off.');
  await page.getByText('Copy history', { exact: true }).click();
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
  const app = await startApp(copying(false));
  let f = null;
  await fakeStatus(page, () => null, () => f);
  await openCopy(page, app.url);
  await tab(page, 'Free up space').click();
  const file = page.locator('#x-file');
  const live = page.locator('#x-live');
  await expect(file).toBeHidden();
  // The line is outside the tab's one status region.
  await expect(page.locator('#panel-free [role=status]')).toHaveCount(1);

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

  // A delete first checks the checked files again, a stage of its own.
  f = { state: 'deleting', recheck: true, title: 'Checking 2 files again before deleting…', progress: { done: 0, total: 2 }, file: 'Listing MyWorks/M 31…' };
  await expect(title(page)).toHaveText('Checking 2 files again before deleting…');
  await expect(live).toHaveText('Checking 2 files again before deleting…');
  await expect(file).toHaveText('Listing MyWorks/M 31…');
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

test("the header's bubble shows each stage of a check and a delete, opens Free up space, and hides after", async ({ page, startApp }) => {
  const app = await startApp(copying(false));
  let f = null, c = null;
  await fakeStatus(page, () => c, () => f);
  await openCopy(page, app.url);
  const chip = page.locator('#free-chip');
  const live = page.locator('#chip-live');
  // Copying is off and nothing runs: no bubble at all.
  await expect(page.locator('#c-title')).toHaveText('Copying is off.');
  await expect(page.locator('#copy-chip')).toBeHidden();
  await expect(chip).toBeHidden();
  // Each change to the header's status region is counted.
  await page.evaluate(() => {
    window.liveChanges = 0;
    new MutationObserver(() => window.liveChanges++).observe(document.getElementById('chip-live'), { childList: true, characterData: true, subtree: true });
  });
  const changes = () => page.evaluate(() => window.liveChanges);
  // On another tab, each stage's start is announced once; its counts are not.
  for (const [view, text] of [
    [{ state: 'checking', title: 'Checking files…', chip: 'Checking what to delete' }, 'Checking what to delete'],
    [{ state: 'deleting', recheck: true, title: 'Checking 2 files again before deleting…', chip: 'Checking 2 files again', progress: { done: 0, total: 2 } }, 'Checking 2 files again'],
    [{ state: 'deleting', title: 'Deleting 1 of 2…', chip: 'Deleting 1 of 2', progress: { done: 0, total: 2 } }, 'Deleting 1 of 2'],
  ]) {
    f = view;
    await expect(chip).toHaveText(text);
    await expect(chip).toBeVisible();
    await expect(live).toHaveText(text);
  }
  // A count changing is not announced.
  const before = await changes();
  f = { state: 'deleting', title: 'Deleting 2 of 2…', chip: 'Deleting 2 of 2', progress: { done: 1, total: 2 } };
  await expect(chip).toHaveText('Deleting 2 of 2');
  await expect(live).toHaveText('Deleting 1 of 2');
  await page.waitForTimeout(1500);
  expect(await changes()).toBe(before);
  // A bubble is a button that opens its tab.
  await page.getByRole('button', { name: 'Deleting 2 of 2' }).click();
  await expect(page.getByRole('tabpanel', { name: 'Free up space' })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Free up space' })).toBeFocused();
  // The announcement goes with the run.
  f = { state: 'done', title: 'Deleted 2 files.' };
  await expect(chip).toBeHidden();
  await expect(live).toHaveText('');
  // On the run's own tab, its status region announces it, not the header.
  f = { state: 'checking', title: 'Checking files…', chip: 'Checking what to delete' };
  await expect(chip).toHaveText('Checking what to delete');
  await expect(page.locator('#x-live')).toHaveText('Checking files…');
  await expect(live).toHaveText('');
  f = null;
  await expect(chip).toBeHidden();
  // From another tab, a second check after an idle spell is announced
  // again, worded as the first.
  await tab(page, 'Copy').click();
  for (let i = 0; i < 2; i++) {
    const n = await changes();
    f = { state: 'checking', title: 'Checking files…', chip: 'Checking what to delete' };
    await expect(live).toHaveText('Checking what to delete');
    expect(await changes()).toBeGreaterThan(n);
    f = null;
    await expect(chip).toBeHidden();
    await expect(live).toHaveText('');
  }
  // Copying's bubble shows while a pass runs, and opens Copy.
  await tab(page, 'Free up space').click();
  c = { state: 'copying', title: 'Copying 1 of 3 files', chip: 'Copying' };
  await page.getByRole('button', { name: 'Copying', exact: true }).click();
  await expect(page.getByRole('tabpanel', { name: 'Copy' })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'Copy' })).toBeFocused();
  c = null;
  await expect(page.locator('#copy-chip')).toBeHidden();
});
