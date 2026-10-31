// Issues: where each finding comes from, keyboard triage, fixing one in a
// linked task and following it while the task is in progress.

import { expect, test } from './support/daemon';

test('issues label where each finding comes from', async ({ page }) => {
  await page.goto('/#/issues');
  const table = page.getByRole('table');
  await expect(table.getByRole('row', { name: /size limit/ })).toContainText('Security rule');
  await expect(table.getByRole('row', { name: /size limit/ })).toContainText('change_handler.go:88 · ChangeHandler.Create');
  await expect(table.getByRole('row', { name: /ignores cancellation/ })).toContainText('AI suggestion');
});

test('issues are triaged from the keyboard, filtered by kind and fixed in a linked task', async ({ page, daemon }) => {
  await page.goto('/#/issues');
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row')).toHaveCount(3);
  await page.keyboard.press('j');
  const detail = page.getByRole('complementary', { name: 'Issue detail' });
  await expect(detail).toContainText('Request body read without a size limit');
  await page.keyboard.press('j');
  await expect(detail).toContainText('Retry loop ignores cancellation');
  await page.keyboard.press('Escape');
  await expect(detail).toHaveCount(0);

  await page.getByRole('group', { name: 'Category' }).getByRole('button', { name: /Security/ }).click();
  await expect(page).toHaveURL(/category=security/);
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row')).toHaveCount(2);
  await page.keyboard.press('j');
  await page.keyboard.press('f');
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('dialog').getByPlaceholder('What should change?')).toHaveValue('Fix: Request body read without a size limit');
  // The daemon drafts the task, so every language gets its own test file convention.
  expect(daemon.requests.filter(request => request.path === '/api/projects/current/findings/f1/task')).toHaveLength(1);
  await page.getByRole('dialog').getByRole('button', { name: 'Save draft' }).click();
  await expect(page.getByText('Draft saved.')).toBeVisible();
  expect(daemon.mutations('/changes')[0].body).toMatchObject({ finding_targets: [{ project_id: 'p1', kind: 'semantic', id: 'f1', path: 'internal/api/handlers/change_handler.go' }] });
});

test('an issue a task is fixing leaves the open issues, links to its task and returns when the task is deleted', async ({ page }) => {
  await page.goto('/#/issues?id=f1');
  await page.getByRole('complementary', { name: 'Issue detail' }).getByRole('button', { name: /Fix with agents/ }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Save draft' }).click();
  await expect(page.getByText('Draft saved.')).toBeVisible();

  await page.goto('/#/issues');
  const table = page.getByRole('table', { name: 'Issues' });
  await expect(table.getByRole('row')).toHaveCount(2);
  await expect(table).not.toContainText('Request body read without a size limit');
  const status = page.getByRole('radiogroup', { name: 'Status' });
  await status.getByRole('radio', { name: /In progress/ }).click();
  await expect(page).toHaveURL(/status=in_progress/);
  await expect(table.getByRole('row')).toHaveCount(2);
  await expect(table).toContainText('In progress · security');
  await table.getByRole('row', { name: /Request body read without a size limit/ }).click();
  const detail = page.getByRole('complementary', { name: 'Issue detail' });
  await expect(detail).toContainText('Being fixed in “Fix: Request body read without a size limit”');
  await expect(detail.getByRole('button', { name: /Fix with agents/ })).toHaveCount(0);
  await detail.getByRole('button', { name: /Open task/ }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-100/);

  await page.getByRole('button', { name: 'Delete task' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete task' }).click();
  await expect(page.getByText('Task deleted.')).toBeVisible();
  await page.goto('/#/issues');
  await expect(page.getByRole('table', { name: 'Issues' })).toContainText('Request body read without a size limit');
});

test('an issue without a file opens a composer that says what Start agents needs', async ({ page, daemon }) => {
  daemon.serve('/api/projects/current/findings', () => ({
    project_id: 'p1', project_revision: 'r42',
    findings: [{ id: 'f9', project_id: 'p1', source: 'ai', severity: 'high', title: 'Vault save is not atomic', message: 'The key file and the vault can disagree after a failed save.', location: {}, status: 'open', category: 'bugs' }],
  }));
  await page.goto('/#/issues?id=f9');
  await page.getByRole('complementary', { name: 'Issue detail' }).getByRole('button', { name: /Fix with agents/ }).click();
  const dialog = page.getByRole('dialog');
  const start = dialog.getByRole('button', { name: /Start agents/ });
  await expect(start).toBeDisabled();
  await expect(dialog.getByRole('status')).toHaveText('Choose at least one file the agents may change.');
  // In its own worktree a testing agent writes the tests it needs, so a file without one is enough.
  await dialog.getByLabel('Search files to add').fill('README');
  await page.keyboard.press('Enter');
  await expect(dialog.getByText('README.md')).toBeVisible();
  await expect(start).toBeEnabled();
  await expect(dialog.getByRole('status')).toHaveCount(0);
  await dialog.getByLabel('Work in its own worktree').uncheck();
  await expect(start).toBeDisabled();
  await expect(dialog.getByRole('status')).toHaveText('Add a test file to the scope so the testing agent can write tests.');
});
