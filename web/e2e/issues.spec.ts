// Issues: where each finding comes from, keyboard triage, fixing one in a
// linked task and following it while the task is in progress.

import { expect, test } from './support/daemon';
import * as data from './support/fixtures';

test("issues group by file, and a file's open issues are fixed in one task", async ({ page, daemon }) => {
  const second = { ...data.findings[0], id: 'f4', severity: 'medium', source: 'ai', title: 'Request ID not logged', message: 'Errors lose the request ID.', location: { path: 'internal/api/handlers/change_handler.go', start_line: 120 }, evidence: undefined };
  daemon.serve('/api/projects/current/findings', () => ({ ...data.project, findings: [...data.findings, second] }));
  await page.goto('/#/issues');
  const file = page.getByRole('rowgroup', { name: 'internal/api/handlers/change_handler.go' });
  await expect(file.getByRole('row')).toHaveCount(3);
  await expect(file.getByRole('row', { name: /size limit/ })).toContainText('L88 · ChangeHandler.Create');
  await expect(page.getByRole('rowgroup', { name: 'internal/llm/client.go' }).getByRole('button', { name: /together/ })).toHaveCount(0);
  await file.getByRole('button', { name: 'Fix 2 together' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByPlaceholder('What should change?')).toHaveValue('Fix 2 issues in internal/api/handlers/change_handler.go');
  await expect(dialog.getByRole('note')).toHaveText('Agents fix these 2 issues one at a time, with a commit for each; one they cannot fix is left out and opens again.');
  await dialog.getByRole('button', { name: 'Save draft' }).click();
  await expect(page.getByText('Draft saved.')).toBeVisible();
  expect(daemon.requests.filter(request => request.path === '/api/projects/current/findings/task')).toHaveLength(1);
  expect(daemon.mutations('/changes')[0].body).toMatchObject({ finding_targets: [{ id: 'f1' }, { id: 'f4' }], issues: [{ finding_id: 'f1', path: 'internal/api/handlers/change_handler.go' }, { finding_id: 'f4' }] });

  await page.goto('/#/issues');
  await page.getByRole('radiogroup', { name: 'Group issues' }).getByRole('radio', { name: 'By severity' }).click();
  await expect(page).toHaveURL(/view=list/);
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row', { name: /ignores cancellation/ })).toContainText('internal/llm/client.go:212');
});

test('a file with more than five open issues fixes five of them in one task', async ({ page, daemon }) => {
  const more = [1, 2, 3, 4, 5].map(n => ({ ...data.findings[0], id: `g${n}`, severity: 'medium', source: 'ai', title: `Issue ${n}`, message: `Problem ${n}.`, location: { path: 'internal/api/handlers/change_handler.go', start_line: 100 + n }, evidence: undefined }));
  daemon.serve('/api/projects/current/findings', () => ({ ...data.project, findings: [...data.findings, ...more] }));
  await page.goto('/#/issues');
  await page.getByRole('rowgroup', { name: 'internal/api/handlers/change_handler.go' }).getByRole('button', { name: 'Fix 5 of 6 together' }).click();
  await expect(page.getByRole('dialog').getByPlaceholder('What should change?')).toHaveValue('Fix 5 issues in internal/api/handlers/change_handler.go');
});

test('issues label where each finding comes from', async ({ page }) => {
  await page.goto('/#/issues?view=list');
  const table = page.getByRole('table');
  await expect(table.getByRole('row', { name: /size limit/ })).toContainText('Security rule');
  await expect(table.getByRole('row', { name: /size limit/ })).toContainText('change_handler.go:88 · ChangeHandler.Create');
  await expect(table.getByRole('row', { name: /ignores cancellation/ })).toContainText('AI suggestion');
});

test("an issue's evidence reads as sections, with its actions above them", async ({ page }) => {
  await page.goto('/#/issues?id=f1');
  const detail = page.getByRole('complementary', { name: 'Issue detail' });
  await expect(detail.getByRole('term')).toHaveText(['Preconditions', 'Fix', 'How to verify']);
  await expect(detail.getByRole('definition').nth(1)).toHaveText('Wrap the body in http.MaxBytesReader before reading it.');
  const fix = await detail.getByRole('button', { name: /Fix with agents/ }).boundingBox();
  const evidence = await detail.getByRole('term').first().boundingBox();
  expect(fix!.y).toBeLessThan(evidence!.y);
});

test('issues are triaged from the keyboard, filtered by kind and fixed in a linked task', async ({ page, daemon }) => {
  await page.goto('/#/issues');
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row')).toHaveCount(5);
  await page.keyboard.press('j');
  const detail = page.getByRole('complementary', { name: 'Issue detail' });
  await expect(detail).toContainText('Request body read without a size limit');
  await page.keyboard.press('j');
  await expect(detail).toContainText('Retry loop ignores cancellation');
  await page.keyboard.press('Escape');
  await expect(detail).toHaveCount(0);

  await page.getByRole('group', { name: 'Category' }).getByRole('button', { name: /Security/ }).click();
  await expect(page).toHaveURL(/category=security/);
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row')).toHaveCount(3);
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
  await expect(table.getByRole('row')).toHaveCount(3);
  await expect(table).not.toContainText('Request body read without a size limit');
  const status = page.getByRole('radiogroup', { name: 'Status' });
  await status.getByRole('radio', { name: /In progress/ }).click();
  await expect(page).toHaveURL(/status=in_progress/);
  await expect(table.getByRole('row')).toHaveCount(3);
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
