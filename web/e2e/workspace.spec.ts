// Navigation and the supporting workspaces: the inbox, the command palette,
// creating a task, branches and issues.

import { expect, test } from './support/daemon';

test('the inbox summarizes what needs you and opens a task', async ({ page }) => {
  await page.goto('/#/');
  await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible();
  await expect(page.getByText('1 task to review · 1 agent run in progress · 1 failed or stale · 1 branch ready to merge')).toBeVisible();
  await page.getByRole('region', { name: 'Needs your review' }).getByText('Reject nested asset paths').click();
  await expect(page).toHaveURL(/#\/tasks\/chg-1$/);
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Reject nested asset paths in the Web UI handler');
});

test('g-sequences and the palette move between workspaces', async ({ page }) => {
  await page.goto('/#/');
  await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible();
  await page.keyboard.press('g');
  await page.keyboard.press('i');
  await expect(page).toHaveURL(/#\/issues/);

  await page.keyboard.press('Control+k');
  await page.getByPlaceholder(/search files, tasks/i).fill('webui.go');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/code\?file=internal%2Fapi%2Fhandlers%2Fwebui\.go|#\/code\?file=internal\/api\/handlers\/webui\.go/);
  await expect(page.getByText('func ServeAsset() {}')).toBeVisible();
});

test('creates a draft task with a scope and criteria', async ({ page, daemon }) => {
  await page.goto('/#/tasks');
  await expect(page.getByRole('heading', { name: 'Tasks' })).toBeVisible();
  await page.keyboard.press('c');
  const dialog = page.getByRole('dialog');
  await dialog.getByPlaceholder('What should change?').fill('Return 404 for missing assets');
  await dialog.getByLabel('Search files to add').fill('webui');
  await dialog.getByRole('option', { name: /internal\/api\/handlers\/webui\.go/ }).first().click();
  await dialog.getByPlaceholder(/e\.g\. Empty input/).fill('Missing assets return 404');
  await dialog.getByRole('button', { name: 'Add' }).click();
  await dialog.getByRole('button', { name: 'Save draft' }).click();

  await expect(page.getByText('Draft saved. It has its own worktree on orca/return-404-for-missing-a1b2c3.')).toBeVisible();
  const [create] = daemon.mutations('/changes');
  expect(create.body).toMatchObject({ project_id: 'p1', title: 'Return 404 for missing assets', acceptance_criteria: ['Missing assets return 404'], worktree: true, confirm_execution: true });
  expect(create.body?.paths).toContain('internal/api/handlers/webui.go');
  await expect(page).toHaveURL(/#\/tasks\/chg-100\?wt=%2Fwork%2Fmini-orca%2F.worktrees%2Freturn-404-for-missing-a1b2c3$/);
  await expect(page.getByRole('complementary', { name: 'Review gate' }).getByText('return-404-for-missing-a1b2c3')).toBeVisible();
});

test('a task in its own worktree does not switch the workspace pages', async ({ page }) => {
  await page.goto('/#/issues');
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row')).toHaveCount(3);
  await page.keyboard.press('j');
  await page.keyboard.press('f');
  await page.getByRole('dialog').getByRole('button', { name: 'Save draft' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-100\?wt=/);
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('worktree');

  await page.keyboard.press('g');
  await page.keyboard.press('i');
  await expect(page).toHaveURL(/#\/issues$/);
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row')).toHaveCount(3);
  await page.getByRole('navigation', { name: 'Workspace' }).getByRole('link', { name: /Tasks/ }).click();
  await expect(page).toHaveURL(/#\/tasks$/);
  await page.getByRole('listitem').filter({ hasText: 'Fix: Request body read without a size limit' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-100\?wt=/);
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('button', { name: 'Tasks' }).click();
  await expect(page).toHaveURL(/#\/tasks$/);
});

test('a task can stay in the main checkout instead of its own worktree', async ({ page, daemon }) => {
  await page.goto('/#/tasks');
  await page.keyboard.press('c');
  const dialog = page.getByRole('dialog');
  await dialog.getByPlaceholder('What should change?').fill('Return 404 for missing assets');
  await dialog.getByLabel('Search files to add').fill('webui');
  await dialog.getByRole('option', { name: /internal\/api\/handlers\/webui\.go/ }).first().click();
  const own = dialog.getByRole('checkbox', { name: 'Work in its own worktree' });
  await expect(own).toBeChecked();
  await own.uncheck();
  await dialog.getByRole('button', { name: 'Save draft' }).click();

  await expect(page.getByText('Draft saved.', { exact: true })).toBeVisible();
  expect(daemon.mutations('/changes')[0].body?.worktree).toBeUndefined();
  await expect(page).toHaveURL(/#\/tasks\/chg-100$/);
});

test('deleting a task removes the worktree created for it', async ({ page, daemon }) => {
  await page.goto('/#/tasks');
  await page.keyboard.press('c');
  const dialog = page.getByRole('dialog');
  await dialog.getByPlaceholder('What should change?').fill('Drop the unused helper');
  await dialog.getByLabel('Search files to add').fill('webui');
  await dialog.getByRole('option', { name: /internal\/api\/handlers\/webui\.go/ }).first().click();
  await dialog.getByRole('button', { name: 'Save draft' }).click();
  await expect(page).toHaveURL(/wt=/);

  await page.getByRole('button', { name: 'Delete task' }).click();
  await expect(page.getByRole('alertdialog')).toContainText('A worktree left with nothing to merge is removed with its branch.');
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete task' }).click();
  await expect(page.getByText('Task deleted. Its worktree was removed too.')).toBeVisible();
  expect(daemon.mutations('/discard-worktree')[0].body).toMatchObject({ path: '/work/mini-orca/.worktrees/drop-the-unused-helper-a1b2c3', source_branch: 'orca/drop-the-unused-helper-a1b2c3', source_head: '41030b7e' });
  await expect(page).toHaveURL(/#\/tasks$/);
});

test('a task scope can name a file that does not exist yet', async ({ page, daemon }) => {
  await page.goto('/#/tasks');
  await page.keyboard.press('c');
  const dialog = page.getByRole('dialog');
  await dialog.getByPlaceholder('What should change?').fill('Move the shared test helpers into their own file');
  await dialog.getByLabel('Search files to add').fill('webui_test');
  await expect(dialog.getByRole('option', { name: /New file/ })).toHaveCount(0);
  await dialog.getByRole('option', { name: /internal\/api\/handlers\/webui_test\.go/ }).click();
  await dialog.getByLabel('Search files to add').fill('./internal/api/handlers/helpers_test.go');
  await dialog.getByRole('option', { name: 'New file internal/api/handlers/helpers_test.go' }).click();
  await expect(dialog.getByRole('listitem').filter({ hasText: 'helpers_test.go' })).toContainText('new file');
  await dialog.getByRole('button', { name: 'Save draft' }).click();

  await expect(page.getByText('Draft saved.')).toBeVisible();
  const [create] = daemon.mutations('/changes');
  expect(create.body?.paths).toEqual(['internal/api/handlers/webui_test.go', 'internal/api/handlers/helpers_test.go']);
});

test('branches explain why a worktree cannot be merged yet', async ({ page }) => {
  await page.goto('/#/git');
  await page.getByRole('listitem').filter({ hasText: 'mo/chg-2' }).click();
  const detail = page.getByRole('complementary', { name: 'Worktree' });
  await expect(detail.getByText('No new commits')).toBeVisible();
  await expect(detail.getByText('This branch has no commits that the main branch lacks yet.')).toBeVisible();
  await expect(detail.getByRole('button', { name: /merge into/i })).toHaveCount(0);

  await page.getByRole('listitem').filter({ hasText: 'mo/chg-7' }).click();
  await expect(detail.getByRole('button', { name: 'Merge into main' })).toBeEnabled();
  await expect(detail.getByText('1 overlapping file')).toBeVisible();
});

test('a merge keeps or removes the worktree as Branches says, for every merge', async ({ page, daemon }) => {
  await page.goto('/#/git');
  const removeOnMerge = page.getByRole('checkbox', { name: 'Delete the worktree and its branch after merging' });
  await expect(removeOnMerge).toBeChecked();
  await removeOnMerge.uncheck();
  await page.reload();
  await expect(removeOnMerge).not.toBeChecked();

  await page.getByRole('listitem').filter({ hasText: 'mo/chg-7' }).click();
  const detail = page.getByRole('complementary', { name: 'Worktree' });
  await detail.getByRole('button', { name: 'Merge into main' }).click();
  await expect(page.getByRole('alertdialog')).toContainText('keeps the worktree and its branch');
  await page.getByRole('alertdialog').getByRole('button', { name: 'Merge' }).click();
  await expect(page.getByText('Merged mo/chg-7.')).toBeVisible();
  expect(daemon.mutations('/git/merge')[0].body).toMatchObject({ path: '/work/.mini-orca/wt/chg-7', keep_worktree: true });

  // The kept worktree has nothing left to merge and can be removed later.
  await page.getByRole('listitem').filter({ hasText: 'mo/chg-7' }).click();
  await detail.getByRole('button', { name: 'Remove worktree' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove worktree' }).click();
  await expect(page.getByText('Removed mo/chg-7.')).toBeVisible();
  expect(daemon.mutations('/discard-worktree')[0].body).toMatchObject({ path: '/work/.mini-orca/wt/chg-7', source_branch: 'mo/chg-7' });
  await expect(page.getByRole('listitem').filter({ hasText: 'mo/chg-7' })).toHaveCount(0);
});

test('a worktree merged outside Mini-Orca is cleaned up with its issues marked done', async ({ page, daemon }) => {
  const applied = { id: 'chg-8', project_id: 'p1', project_revision: 'r1', kind: 'fix', title: 'Escape asset names', revision: 2, hash: 'h8', state: 'applied', freshness: 'current', updated_at: '2026-10-29T00:00:00Z', commit: { state: 'committed', oid: 'cc33' } };
  daemon.addWorktree({ path: '/work/.mini-orca/wt/chg-8', head: 'cc33', branch: 'mo/chg-8', integration: 'merged', mergeable: false, dirty: false, sessions: [applied], completed: [applied] });
  await page.goto('/#/git');
  await page.getByRole('listitem').filter({ hasText: 'mo/chg-8' }).click();
  const detail = page.getByRole('complementary', { name: 'Worktree' });
  await expect(detail.getByText('Merged', { exact: true })).toBeVisible();
  await detail.getByRole('button', { name: 'Remove worktree' }).click();
  await expect(page.getByRole('alertdialog')).toContainText('Its linked issues are marked done, and the history of its 1 applied task goes with it.');
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove worktree' }).click();
  await expect(page.getByText('Removed mo/chg-8.')).toBeVisible();
  expect(daemon.mutations('/cleanup-merged')[0].body).toMatchObject({ path: '/work/.mini-orca/wt/chg-8', source_head: 'cc33', source_branch: 'mo/chg-8' });
  expect(daemon.mutations('/discard-worktree')).toHaveLength(0);
});

test('by default a merge removes the worktree and its branch', async ({ page, daemon }) => {
  await page.goto('/#/git');
  await page.getByRole('listitem').filter({ hasText: 'mo/chg-7' }).click();
  await page.getByRole('complementary', { name: 'Worktree' }).getByRole('button', { name: 'Merge into main' }).click();
  await expect(page.getByRole('alertdialog')).toContainText('then removes the worktree and its branch');
  await page.getByRole('alertdialog').getByRole('button', { name: 'Merge' }).click();
  await expect(page.getByText('Merged mo/chg-7.')).toBeVisible();
  expect(daemon.mutations('/git/merge')[0].body?.keep_worktree).toBeUndefined();
  await expect(page.getByRole('listitem').filter({ hasText: 'mo/chg-7' })).toHaveCount(0);

  // The tests of what the merge changed run on the merged result.
  const checks = page.getByRole('region', { name: 'Checks after the merge' });
  await expect(checks).toContainText('Checks after merging mo/chg-7');
  await expect(checks).toContainText('Passed');
  await expect(checks).toContainText('Only failures from before the change');
  await expect(page.getByText('The checks pass on the merged result.')).toBeVisible();
  expect(daemon.mutations('/git/merge-checks')[0].body).toMatchObject({ project_id: 'p1', from: '41030b7e00000000000000000000000000000000', to: '5e7a9c1d00000000000000000000000000000000' });
  await checks.getByRole('button', { name: 'Dismiss the merge checks' }).click();
  await expect(checks).toHaveCount(0);
});

test('a merge whose result fails its tests says so and keeps the merge', async ({ page, daemon }) => {
  daemon.mergeChecksOutcome = 'failed';
  await page.goto('/#/git');
  await page.getByRole('listitem').filter({ hasText: 'mo/chg-7' }).click();
  await page.getByRole('complementary', { name: 'Worktree' }).getByRole('button', { name: 'Merge into main' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Merge' }).click();
  const checks = page.getByRole('region', { name: 'Checks after the merge' });
  await expect(checks).toContainText('Failed');
  await expect(checks).toContainText('fix it in a new task, or revert the merge commit');
  await expect(checks.getByText('New failures: example/app.TestServe.')).toBeVisible();
  await expect(page.getByRole('alert').filter({ hasText: 'Checks failed after the merge' })).toBeVisible();
});

test('issues label where each finding comes from', async ({ page }) => {
  await page.goto('/#/issues');
  const table = page.getByRole('table');
  await expect(table.getByRole('row', { name: /size limit/ })).toContainText('Security rule');
  await expect(table.getByRole('row', { name: /ignores cancellation/ })).toContainText('AI suggestion');
});

for (const theme of ['dark', 'light'] as const) {
  test(`every workspace renders in the ${theme} theme`, async ({ page }) => {
    await page.addInitScript(value => localStorage.setItem('mini-orca.prefs.v2', JSON.stringify({ theme: value })), theme);
    for (const [hash, heading] of [['/#/', 'Inbox'], ['/#/tasks', 'Tasks'], ['/#/issues', 'Issues'], ['/#/git', 'Branches'], ['/#/settings', 'Project']] as const) {
      await page.goto(hash);
      await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
    }
  });
}

test('the handbook says when the project overview is missing and shows recorded decisions', async ({ page }) => {
  await page.goto('/#/docs/decisions');
  await expect(page.getByText('No project overview yet')).toBeVisible();
  await expect(page.getByText(/below are read from the code/)).toBeVisible();
  const decision = page.locator('.doc-card', { hasText: 'Embed the Web UI in the daemon' });
  await expect(decision).toContainText('Architecture');
  await expect(decision).toContainText('Tradeoff: Every UI change needs a rebuilt bundle.');
});

test('the docs draw the components and their imports from the code', async ({ page }) => {
  await page.goto('/#/docs/overview');
  const diagram = page.getByRole('img', { name: '5 components and the imports between them' });
  await expect(diagram).toBeVisible();
  await expect(page.getByText('Drawn from the code')).toBeVisible();
  await expect(page.getByRole('button', { name: 'cmd/daemon/main.go' })).toBeVisible();
  await diagram.getByRole('button', { name: /^App service, internal\/app/ }).click();
  await expect(page).toHaveURL(/#\/docs\/components\?path=internal%2Fapp/);
  const card = page.locator('.doc-card.is-focus');
  await expect(card).toContainText('Workflow orchestration.');
  await expect(card).toContainText('From the project overview (model)');
  await expect(card).toContainText('Used by');
  await card.getByRole('button', { name: 'Project' }).click();
  await expect(page.locator('.doc-card.is-focus')).toContainText('internal/project');
});

test('the docs refresh asks first, runs in the background and says how it ended', async ({ page, daemon }) => {
  daemon.serveDocsRefresh({ status: 'completed', components: 5, summarized: 2, kept: 3, overview: 'written', at: new Date().toISOString() });
  await page.goto('/#/docs/overview');
  await expect(page.getByRole('status').filter({ hasText: 'Refresh just now: 2 summaries written, 3 kept, overview written' })).toBeVisible();
  await page.getByRole('button', { name: 'Refresh docs' }).click();
  const dialog = page.getByRole('alertdialog', { name: 'Refresh the docs?' });
  await expect(dialog).toContainText('gemini-2.5-pro summarizes again the components whose files');
  await expect(dialog).toContainText('go to generativelanguage.googleapis.com');
  await dialog.getByRole('button', { name: 'Refresh docs' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Refreshing the docs…' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh docs' })).toBeDisabled();
  const [start] = daemon.mutations('/docs/refresh');
  expect(start.body).toMatchObject({ project_id: 'p1', confirm_remote_provider: true });
});

test('the codebase answers a question from the palette with lines that open in Code', async ({ page, daemon }) => {
  await page.goto('/#/docs/overview');
  await page.keyboard.press('Control+k');
  await page.getByPlaceholder(/search files, tasks/i).fill('how are assets served');
  await page.getByRole('option', { name: /Ask the codebase: “how are assets served”/ }).click();
  await expect(page).toHaveURL(/#\/docs\/ask\?q=how/);
  await expect(page.getByLabel('Question about the code')).toHaveValue('how are assets served');
  await page.getByRole('button', { name: 'Ask', exact: true }).click();
  const dialog = page.getByRole('alertdialog', { name: 'Ask the codebase?' });
  await expect(dialog).toContainText('reads the project');
  await dialog.getByRole('button', { name: 'Ask' }).click();
  const answer = page.getByRole('region', { name: 'Answer to: how are assets served' });
  await expect(answer).toContainText('a nested path is rejected');
  await expect(answer).toContainText('claude:opus · 4 turns · 7 tool calls');
  expect(daemon.mutations('/ask')[0].body).toMatchObject({ project_id: 'p1', question: 'how are assets served', confirm_remote_provider: true });
  await answer.getByRole('button', { name: /internal\/api\/handlers\/webui\.go:1–3/ }).click();
  await expect(page).toHaveURL(/#\/code\?file=internal%2Fapi%2Fhandlers%2Fwebui\.go&line=1/);
  await page.goBack();
  await expect(page.getByRole('region', { name: 'Answer to: how are assets served' })).toBeVisible();
});

test('usage lists what agents spent per task and the task shows its own spend', async ({ page }) => {
  await page.goto('/#/settings/usage');
  const table = page.getByRole('table', { name: 'Usage by task' });
  await expect(table.getByRole('row').filter({ hasText: 'Reject nested asset paths' })).toContainText('$0.194');
  await expect(table.getByRole('row').filter({ hasText: 'Total' })).toContainText('≥ $0.575');
  await expect(page.getByText('Some models have no configured rate.')).toBeVisible();
  await table.getByRole('row').filter({ hasText: 'Reject nested asset paths' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-1/);
  await expect(page.getByRole('complementary', { name: 'Review gate' })).toContainText('6 calls · 65k tokens · $0.194');
  await expect(page.getByRole('region', { name: 'Agent run' })).toContainText('21k tokens · $0.061');
  await expect(page.getByRole('region', { name: 'Agent run' })).toContainText('Worked with tools: 14 calls in 9 turns (read_file 6, edit_file 4, search 2, run_checks 1, finish 1)');
});

test('a finished agent run is announced with its spend', async ({ page, daemon }) => {
  daemon.push({ kind: 'workflow', session_id: 'chg-1', workflow_id: 'wf-1', title: 'Reject nested asset paths in the Web UI handler', status: 'awaiting_human_review', usage: { model_calls: 6, input_tokens: 59_200, output_tokens: 5_300 }, cost: { status: 'estimated', currency: 'USD', known_subtotal: 0.194, total: 0.194, reasons: [] } });
  await page.goto('/#/tasks');
  await expect(page.getByText('Agents finished “Reject nested asset paths in the Web UI handler”. It needs your review.')).toBeVisible();
  await expect(page.getByText('6 calls · 65k tokens · $0.194')).toBeVisible();
});

test('set-aside files are announced and explained with their remedy', async ({ page, daemon }) => {
  daemon.serve('/api/projects/current/quarantine', () => ({
    files: [{ kind: 'agent_journal', file: '.mini-orca/agents/ab12/journal.json', quarantined_as: '.mini-orca/agents/ab12/journal.quarantined-20261027T101500Z.json', reason: 'it is not valid JSON', remedy: 'Mini-Orca no longer reads the set-aside file. Inspect it if you need its history, then delete it.', at: '2026-10-27T10:15:00Z' }],
    skipped: [{ kind: 'task', file: '.mini-orca/changes/change-9f.json', reason: 'written by a newer Mini-Orca', remedy: 'Upgrade Mini-Orca to use it, or move it away.' }],
  }));
  await page.goto('/#/');
  await page.getByRole('button', { name: 'Review' }).click();
  await expect(page).toHaveURL(/#\/settings\/project/);
  const list = page.getByRole('region', { name: 'Set-aside files' });
  await expect(list).toContainText('.mini-orca/agents/ab12/journal.json');
  await expect(list).toContainText('It is not valid JSON. Its bytes are in .mini-orca/agents/ab12/journal.quarantined-20261027T101500Z.json.');
  await expect(list).toContainText('Upgrade Mini-Orca to use it, or move it away.');
});

test('agent roles can run with any listed model and a project budget', async ({ page, daemon }) => {
  await page.goto('/#/settings/models');
  const card = page.getByRole('region', { name: 'Agent roles' });
  await expect(card).toContainText('From the configuration');
  const review = card.getByLabel('Reviews');
  await expect(review).toHaveValue('analyze');
  await expect(review.getByRole('option', { name: 'Gemini 3.8 Pro (agy:gemini-3.8-pro-high)' })).toBeAttached();
  await expect(card).toContainText('Pi models could not be loaded.');

  await review.selectOption('agy:gemini-3.8-pro-high');
  await expect(card.getByText('remote · cli://agy')).toBeVisible();
  const calls = card.getByLabel('Model calls');
  await calls.fill('50');
  await expect(card.getByText('At most 40 (config.yaml).')).toBeVisible();
  await expect(card.getByRole('button', { name: 'Save' })).toBeDisabled();
  await calls.fill('20');
  await card.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Agent settings saved. New runs use them.')).toBeVisible();
  await expect(card).toContainText('Project settings');
  const [saved] = daemon.mutations('/agent-settings/control');
  expect(saved.body).toMatchObject({ action: 'replace', confirm: true, version: 0, configuration: 'c'.repeat(64), values: { roles: { creation: 'bug', testing: 'code', review: 'agy:gemini-3.8-pro-high' }, disabled_skills: [] } });
  expect((saved.body!.values as { limits: Record<string, number> }).limits.model_calls).toBe(20);
  await expect(review).toHaveValue('agy:gemini-3.8-pro-high');

  await card.getByRole('button', { name: 'Use the configuration' }).click();
  await expect(card).toContainText('From the configuration');
  await expect(review).toHaveValue('analyze');
  expect(daemon.mutations('/agent-settings/control')[1].body).toMatchObject({ action: 'reset', version: 1 });
});

test('a daemon without config.yaml warns that it uses the default models', async ({ page, daemon }) => {
  daemon.serve('/api/models/current', () => ({ scopes: {}, config_file: '' }));
  await page.goto('/#/');
  await page.getByRole('button', { name: 'No config.yaml · default models' }).click();
  await expect(page).toHaveURL(/#\/settings\/models/);
  await expect(page.getByText('No configuration was found')).toBeVisible();
});

test('stale and failed tasks can be deleted from the list and the task page', async ({ page, daemon }) => {
  daemon.addTask({ project_id: 'p1', project_revision: 'r41', id: 'chg-8', kind: 'fix', title: 'Trim the import log', revision: 3, hash: 'h8', state: 'draft', freshness: 'stale', updated_at: '2026-10-20T10:00:00Z', paths: ['internal/project/import.go'] });
  await page.goto('/#/tasks');
  const attention = page.getByRole('region', { name: 'Failed or stale' });
  await expect(attention.getByRole('listitem')).toHaveCount(2);
  await attention.getByRole('button', { name: 'Delete all 2' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('“Cache the parsed index”, “Trim the import log”');
  await dialog.getByRole('button', { name: 'Delete 2 tasks' }).click();
  await expect(page.getByText('2 tasks deleted.')).toBeVisible();
  await expect(attention).toHaveCount(0);
  expect(daemon.mutations('/delete').map(request => request.body)).toEqual([
    { project_id: 'p1', project_revision: 'r42', revision: 2, hash: 'x' },
    { project_id: 'p1', project_revision: 'r41', revision: 3, hash: 'h8' },
  ]);

  await page.goto('/#/tasks/chg-1');
  await page.getByRole('button', { name: 'Delete task' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete task' }).click();
  await expect(page).toHaveURL(/#\/tasks$/);
  await expect(page.getByText('Reject nested asset paths')).toHaveCount(0);
});

test('a dark palette is chosen in Appearance and kept', async ({ page }) => {
  await page.goto('/#/settings/appearance');
  await page.getByRole('radio', { name: 'Plum' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'plum');
  await expect(page.getByRole('radio', { name: 'Plum' })).toHaveAttribute('aria-checked', 'true');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'plum');
  await page.getByRole('radio', { name: 'Light' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('radio', { name: 'True black' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'oled');
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

test('a task shows its files against the code rules, and a budget issue opens a refactor task', async ({ page, daemon }) => {
  daemon.serve('/api/projects/current/changes/chg-1/rules', () => ({
    session_id: 'chg-1',
    revision: 3,
    problems: [],
    files: [
      { path: 'internal/api/handlers/webui.go', budgets: { file_lines: 800, function_complexity: 15 }, lines: 812, base_lines: 812, debt: [{ kind: 'file_lines', value: 812, base: 812, limit: 800 }], test: false, proposed: true, sources: ['AGENTS.md'] },
      { path: 'internal/api/handlers/mime.go', budgets: { file_lines: 800, function_complexity: 15 }, lines: 40, over: [{ kind: 'function_complexity', function: 'mimeType', line: 3, value: 17, limit: 15 }], test: false, proposed: true, sources: ['AGENTS.md'] },
    ],
  }));
  await page.goto('/#/tasks/chg-1');
  const rules = page.getByRole('region', { name: 'Code rules' });
  await expect(rules).toContainText('from AGENTS.md');
  await expect(rules).toContainText('812 / 800 lines');
  await expect(rules).toContainText('Already over');
  await expect(rules).toContainText('812 lines (max 800)');
  await expect(rules).toContainText('Over budget');
  await expect(rules).toContainText('mimeType (line 3): complexity 17 (max 15)');

  daemon.serve('/api/projects/current/findings', () => ({
    project_id: 'p1',
    project_revision: 'r42',
    findings: [{ id: 'f9', project_id: 'p1', source: 'rules', category: 'maintainability', severity: 'low', title: 'Refactor internal/api/handlers/webui.go to fit its code budgets', message: 'It was already over the budgets in AGENTS.md when a task changed it.', evidence: 'file has 812 lines (max 800)', location: { path: 'internal/api/handlers/webui.go' }, status: 'open' }],
  }));
  await page.goto('/#/issues');
  await expect(page.getByRole('table', { name: 'Issues' }).getByRole('row', { name: /Refactor internal/ })).toContainText('Code budget (AGENTS.md)');
  await page.keyboard.press('j');
  await page.keyboard.press('f');
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByPlaceholder('What should change?')).toHaveValue('Refactor internal/api/handlers/webui.go to fit its code budgets');
  await dialog.getByRole('button', { name: 'Save draft' }).click();
  await expect(page.getByText('Draft saved.')).toBeVisible();
  const created = daemon.mutations('/changes')[0].body as { acceptance_criteria: string[]; paths: string[] };
  expect(created.acceptance_criteria[0]).toContain('splitting it into cohesive sibling files');
  expect(created.paths).toContain('internal/api/handlers/webui.go');
});
