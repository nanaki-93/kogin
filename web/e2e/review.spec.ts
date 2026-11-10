// The core job: review every changed file, then accept this exact version as
// a commit on the task's branch in one step, with the review gate tracking each step.

import { expect, test } from './support/daemon';
import { agentSettings, guidedReviewTask, pausedTask, planReadyTask, rejectedTask, reviewTask, stageModel } from './support/fixtures';

test('reviews the files, then accepts and commits in one step', async ({ page, daemon }) => {
  await page.goto('/#/tasks/chg-1');
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  const header = page.locator('header.task-header');
  await expect(gate.getByText('0 of 3')).toBeVisible();
  // Accept is offered once it can run; until then the header names the next step.
  await expect(gate.getByRole('button', { name: 'Accept & commit' })).toHaveCount(0);
  await expect(header.getByText('Needs review')).toBeVisible();
  // An optional check failure is a warning, not a blocker.
  await expect(gate.getByText('1 optional failed')).toBeVisible();

  await header.getByRole('button', { name: 'Review files' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-1\/files/);
  for (let viewed = 1; viewed <= 3; viewed++) {
    await page.keyboard.press('v');
    await expect(page.getByText(`${viewed}/3 viewed`)).toBeVisible();
  }
  expect(daemon.mutations('/review-file').map(request => request.body?.path)).toEqual([
    'internal/api/handlers/webui.go',
    'internal/api/handlers/webui_test.go',
    'internal/api/handlers/mime.go',
  ]);

  // The next step is in the header of every tab: no detour to approve first.
  await expect(header.getByText('Ready to accept')).toBeVisible();
  await expect(page.getByRole('button', { name: /^Approve/ })).toHaveCount(0);
  await header.getByRole('button', { name: 'Accept & commit' }).click();
  const dialog = page.getByRole('dialog', { name: 'Accept and commit?' });
  await expect(dialog).toContainText('on orca/reject-nested-a1b2c3');
  await expect(dialog).toContainText('records your approval of this version');
  await expect(dialog.getByLabel('Commit message')).toHaveValue('fix: Reject nested asset paths in the Web UI handler');
  await dialog.getByRole('button', { name: 'Accept & commit' }).click();
  await expect(page.getByText('Accepted and committed on orca/reject-nested-a1b2c3.')).toBeVisible();

  const [review] = daemon.mutations('/review');
  expect(review.body).toEqual({ project_id: 'p1', project_revision: 'r42', revision: 3, hash: 'sha256:9f2c1a7be4' });
  const [apply] = daemon.mutations('/apply');
  expect(apply.body).toMatchObject({ project_id: 'p1', project_revision: 'r42', revision: 3, hash: 'sha256:9f2c1a7be4', confirm: true, commit_message: 'fix: Reject nested asset paths in the Web UI handler' });
  await expect(header.getByText('Committed')).toBeVisible();
  await header.getByRole('button', { name: 'Merge from Branches' }).click();
  await expect(page).toHaveURL(/#\/git/);
});

test('a version that changes behavior is accepted only with its behavior changes', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  const change = { title: 'Nested asset paths return 404', path: 'internal/api/handlers/webui.go', line: 42, detail: 'Before: 500. After: 404. The task asked for it.' };
  daemon.sessions.set('chg-1', { ...session, reviewed_files: session.changes.map(c => c.path), workflow: { ...session.workflow!, review: { ...session.workflow!.review!, behavior_changes: [change] } } });
  await page.goto('/#/tasks/chg-1');
  // The review stage lists what the version changes for its users.
  const listed = page.getByRole('group', { name: 'Behavior changes' });
  await expect(listed).toContainText('1 behavior change');
  await expect(listed).toContainText('To accept');
  await page.getByRole('complementary', { name: 'Review gate' }).getByRole('button', { name: 'Accept & commit' }).click();
  const dialog = page.getByRole('dialog', { name: 'Accept and commit?' });
  await expect(dialog).toContainText('Nested asset paths return 404');
  const confirm = dialog.getByRole('button', { name: 'Accept & commit' });
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel('I accept these behavior changes').check();
  await confirm.click();
  await expect(page.getByText('Accepted and committed on orca/reject-nested-a1b2c3.')).toBeVisible();
  expect(daemon.mutations('/review')[0].body).toMatchObject({ hash: 'sha256:9f2c1a7be4', accept_behavior_changes: true });
});

test('cancelling the Accept confirmation approves and commits nothing', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, reviewed_files: session.changes.map(change => change.path) });
  await page.goto('/#/tasks/chg-1');
  await page.getByRole('complementary', { name: 'Review gate' }).getByRole('button', { name: 'Accept & commit' }).click();
  const dialog = page.getByRole('dialog', { name: 'Accept and commit?' });
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  expect(daemon.mutations('/review')).toEqual([]);
  expect(daemon.mutations('/apply')).toEqual([]);
});

test('a version approved before Accept became one step is only committed', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, reviewed_files: session.changes.map(change => change.path), reviewed_hash: session.hash });
  await page.goto('/#/tasks/chg-1');
  await page.locator('header.task-header').getByRole('button', { name: 'Accept & commit' }).click();
  const dialog = page.getByRole('dialog', { name: 'Accept and commit?' });
  await expect(dialog.getByLabel('Approval notes')).toHaveCount(0);
  await dialog.getByLabel('Commit message').fill('fix: reject nested asset names');
  await dialog.getByRole('button', { name: 'Accept & commit' }).click();
  await expect(page.getByText('Accepted and committed on orca/reject-nested-a1b2c3.')).toBeVisible();
  expect(daemon.mutations('/review')).toEqual([]);
  expect(daemon.mutations('/apply')[0].body).toMatchObject({ commit_message: 'fix: reject nested asset names' });
});

test('the task list names the running phase and keeps tasks ready to accept apart', async ({ page, daemon }) => {
  await page.goto('/#/tasks');
  const running = page.getByRole('listitem').filter({ hasText: 'Show per-task model cost' });
  await expect(running).toContainText('Fixing 2 of 3');
  const task = page.getByRole('listitem').filter({ hasText: 'Reject nested asset paths' });
  await expect(task).toContainText('Needs review');

  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, reviewed_files: [session.changes[0].path] });
  await page.reload();
  await expect(task).toContainText('1 of 3 files viewed');

  daemon.sessions.set('chg-1', { ...session, reviewed_files: session.changes.map(change => change.path) });
  await page.reload();
  const ready = page.getByRole('region', { name: 'Ready to accept' });
  await expect(ready.getByRole('listitem')).toHaveCount(1);
  await expect(ready.getByRole('listitem')).toContainText('Reject nested asset paths');
  await expect(ready.getByRole('listitem')).toContainText('Ready to accept');
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0);
});

test('compares the split and unified diff layouts', async ({ page }) => {
  await page.goto('/#/tasks/chg-1/files');
  const diff = page.getByRole('region', { name: /Diff of internal\/api\/handlers\/webui.go/ });
  await expect(diff).toContainText('strings.Contains(name, "/")');
  await page.keyboard.press('s');
  await expect(page.getByRole('button', { name: /unified view/i })).toBeVisible();
  // A line with nothing across from it (a new file here) is as tall as any other row.
  await page.goto('/#/tasks/chg-1/files?file=internal/api/handlers/mime.go');
  const added = page.getByRole('region', { name: /Diff of internal\/api\/handlers\/mime.go/ });
  await expect(added.locator('td.diff-blank').first()).toBeAttached();
  const heights = await added.locator('table.diff-split tr.diff-row').evaluateAll(trs => trs.map(tr => Math.round(tr.getBoundingClientRect().height)));
  expect(heights.length).toBeGreaterThan(0);
  expect(Math.max(...heights)).toBeLessThan(30);
});

test('the review reads one hunk at a time beside the notes of its file and hunk, and marks a file viewed once every hunk is right', async ({ page, daemon }) => {
  await page.goto('/#/tasks/chg-1/files');
  const pane = page.getByRole('complementary', { name: 'This hunk' });
  // Beside the code, one line on what the file's change fixes; it replaces the agent's own.
  await expect(pane.getByRole('region', { name: 'What it fixes' })).toContainText('Nested asset paths no longer reach the embedded read.');
  await expect(page.getByText('Rejects nested names.')).toBeHidden();
  // Beside the hunk, its note: the goal it serves, the wrong behavior it removes, then how.
  await expect(pane).toContainText('Hunk 1 of 3');
  await expect(pane).toContainText('Criterion 1 · Nested asset paths return 404');
  await expect(pane).toContainText('A nested name reached fs.ReadFile and returned 500 instead of 404.');
  // The mechanism folds away until asked for.
  await expect(pane.getByText('ServeAsset refuses names with a slash.')).toBeHidden();
  await pane.getByText('How', { exact: true }).click();
  await expect(pane.getByText('ServeAsset refuses names with a slash.')).toBeVisible();
  await expect(page.locator('.hunk-code')).toContainText('strings.Contains(name, "/")');
  // The review bar says what is left before Accept.
  const bar = page.getByRole('region', { name: 'Review progress' });
  await expect(bar).toContainText('0 of 3 viewed');
  await expect(bar).toContainText('Accept & commit · 3 files left');

  // A: the hunk is right; the file's only hunk, so the file is viewed and the next hunk opens.
  await page.keyboard.press('a');
  await expect.poll(() => daemon.mutations('/review-file').map(request => request.body?.path)).toEqual(['internal/api/handlers/webui.go']);
  await expect(pane).toContainText('Hunk 2 of 3');
  await expect(page.locator('.diff-file-head, .fc-diff-head')).toContainText('webui_test.go');
  await expect(pane).toContainText('No notes for this file.');
  // ← goes back; C opens a comment on the hunk's first changed line.
  await page.keyboard.press('ArrowLeft');
  await expect(pane).toContainText('Hunk 1 of 3');
  await page.keyboard.press('m');
  await expect(page.locator('.hunk-code').getByRole('textbox')).toBeVisible();
});

test('the whole file shows each hunk under its note, and the notes fold away', async ({ page, daemon }) => {
  await page.goto('/#/tasks/chg-1/files');
  await expect(page.getByRole('complementary', { name: 'This hunk' })).toBeVisible();
  await page.keyboard.press('f');
  await expect(page.getByRole('radio', { name: 'Whole file' })).toBeChecked();
  const notes = page.getByRole('region', { name: 'What it fixes' });
  const diff = page.getByRole('region', { name: /Diff of internal\/api\/handlers\/webui.go/ });
  const note = diff.locator('tr.diff-note');
  await expect(note).toHaveCount(1);
  await expect(note).toContainText('Criterion 1 · Nested asset paths return 404');
  await expect(note.locator('code').first()).toHaveText('fs.ReadFile');
  // It heads its hunk: the context leading in, then the replaced line, removed half first.
  await expect(note.locator('xpath=following-sibling::tr[1]')).toContainText('name := trim(r)');
  await expect(note.locator('xpath=following-sibling::tr[2]')).toContainText('if name == "" {');
  // A file without notes says so; asking again writes them for this version.
  await page.keyboard.press('j');
  await expect(notes).toContainText('No notes for this file.');
  await notes.getByRole('button', { name: 'Explain again' }).click();
  // The review model is remote: the diff leaves only once allowed.
  await page.getByRole('alertdialog', { name: 'Send code to a remote model?' }).getByRole('button', { name: 'Send request' }).click();
  await expect(notes).toContainText('Explained internal/api/handlers/webui_test.go.');
  expect(daemon.mutations('/walkthrough')[0].body).toMatchObject({ project_id: 'p1', revision: 3, hash: 'sha256:9f2c1a7be4', confirm_remote_provider: true });
  // The notes fold away and stay folded.
  await page.keyboard.press('k');
  await page.getByRole('button', { name: 'Hide what it fixes' }).click();
  await expect(notes).toBeHidden();
  await expect(page.locator('tr.diff-note')).toHaveCount(0);
  await expect(page.getByText('Rejects nested names.')).toBeVisible();
});

test('overrides the reviewers with a reason, then accepts with notes', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', rejectedTask());
  await page.goto('/#/tasks/chg-1');
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  await expect(gate.getByText('Changes requested')).toBeVisible();
  await expect(gate.getByRole('button', { name: /^Approve/ })).toHaveCount(0);

  // Advisory notes are shown apart from blocking findings.
  await page.getByRole('button', { name: /Correctness/ }).click();
  await expect(page.getByText('Consider a table-driven test.')).toBeVisible();
  await expect(page.getByText('1 blocking finding')).toBeVisible();

  await gate.getByRole('button', { name: 'Override reviewers…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Override the reviewers?' });
  await expect(dialog).toContainText('MIME table belongs in its own package');
  await expect(dialog).toContainText('webui.go:42');
  await expect(dialog.getByRole('button', { name: 'Override reviewers' })).toBeDisabled();
  await dialog.getByLabel('Override reason').fill('The MIME table is private to this handler.');
  await dialog.getByRole('button', { name: 'Override reviewers' }).click();
  await expect(dialog).toBeHidden();
  await expect(gate.getByText('Overridden by you')).toBeVisible();
  await expect(page.getByText('The MIME table is private to this handler.')).toBeVisible();
  expect(daemon.mutations('/review-override')[0].body).toMatchObject({ revision: 3, hash: 'sha256:9f2c1a7be4', workflow_id: 'wf-1', reason: 'The MIME table is private to this handler.' });

  await gate.getByRole('button', { name: 'Accept & commit' }).click();
  const accept = page.getByRole('dialog', { name: 'Accept and commit?' });
  await accept.getByLabel('Approval notes').fill('Revisit the package split next sprint.');
  await accept.getByRole('button', { name: 'Accept & commit' }).click();
  await expect(page.getByText('Accepted and committed on orca/reject-nested-a1b2c3.')).toBeVisible();
  await expect(gate.getByText('Revisit the package split next sprint.')).toBeVisible();
  expect(daemon.mutations('/review')[0].body).toMatchObject({ hash: 'sha256:9f2c1a7be4', notes: 'Revisit the package split next sprint.' });
  expect(daemon.mutations('/apply')).toHaveLength(1);
});

test('checks from an earlier project tree must run again before Accept', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, reviewed_files: session.changes.map(change => change.path), checks_outdated: true });
  await page.goto('/#/tasks/chg-1');
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  await expect(gate.getByText('Outdated')).toBeVisible();
  await expect(gate.getByText('The project changed since checks ran. Run them again before you accept.')).toBeVisible();
  await expect(gate.getByRole('button', { name: 'Accept & commit' })).toHaveCount(0);
  await page.getByRole('tab', { name: /checks/i }).click();
  await expect(page.getByText('These checks ran on an earlier version of the project')).toBeVisible();
});

test('a stale task names the file that changed and is re-analyzed', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, freshness: 'stale', stale_reason: 'go.mod changed since this proposal was made' });
  await page.goto('/#/tasks/chg-1');
  await expect(page.getByText('Out of date: go.mod changed since this proposal was made. Re-analyze it against the current files, or delete it.')).toBeVisible();
  await expect(page.getByRole('button', { name: /run agents again/i })).toHaveCount(0);
  await page.locator('header.task-header').getByRole('button', { name: 'Re-analyze' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('A model checks whether the issue is still present in the current files.');
  await dialog.getByRole('button', { name: 'Re-analyze' }).click();

  await expect(page.getByText('Still valid: the task now matches the current files. Run the agents when ready.')).toBeVisible();
  expect(daemon.mutations('/reanalyze')[0].body).toMatchObject({ project_id: 'p1', revision: session.revision, hash: session.hash, confirm_remote_provider: false });
  await expect(page.locator('header.task-header').getByRole('button', { name: 'Run agents again' })).toHaveCount(0);
  await expect(page.locator('header.task-header').getByRole('button', { name: 'Investigate & plan' })).toBeEnabled();
  await expect(page.getByText('Out of date:')).toHaveCount(0);
});

test('a stale task that is no longer valid is deleted', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, freshness: 'stale', stale_reason: 'internal/api/handlers/webui.go changed since this proposal was made' });
  daemon.reanalysisOutcome = 'not_valid';
  await page.goto('/#/tasks/chg-1');
  await page.locator('header.task-header').getByRole('button', { name: 'Re-analyze' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Re-analyze' }).click();

  await expect(page.getByText(`No longer valid: “${session.title}” was deleted.`)).toBeVisible();
  await expect(page).toHaveURL(/#\/tasks$/);
  await expect(page.getByRole('link', { name: new RegExp(session.title) })).toHaveCount(0);
});

/** Saves a draft task in its own worktree from the composer; it is chg-100. */
async function draftInItsOwnWorktree(page: import('@playwright/test').Page) {
  await page.goto('/#/tasks');
  await page.keyboard.press('c');
  const dialog = page.getByRole('dialog');
  await dialog.getByPlaceholder('What should change?').fill('Reject nested asset paths');
  await dialog.getByLabel('Search files to add').fill('webui');
  await dialog.getByRole('option', { name: /internal\/api\/handlers\/webui\.go/ }).first().click();
  await dialog.getByRole('button', { name: 'Save draft' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-100\?wt=/);
}

test('a task whose files a merge changed is re-analyzed and retaken from main', async ({ page, daemon }) => {
  await draftInItsOwnWorktree(page);
  daemon.overtake('chg-100', ['internal/api/handlers/webui.go']);
  await page.reload();
  const callout = page.getByText('main changed 1 file of this task since it started');
  await expect(callout).toBeVisible();
  await page.getByRole('button', { name: 'Re-analyze & retake' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('main changed internal/api/handlers/webui.go since this task started.');
  await dialog.getByRole('button', { name: 'Re-analyze & retake' }).click();

  await expect(page.getByText('Still valid: “Reject nested asset paths” starts again from main on orca/reject-nested-asset-d4e5f6. Run the agents when ready.')).toBeVisible();
  await expect(page).toHaveURL(/#\/tasks\/chg-101\?wt=.*d4e5f6/);
  expect(daemon.mutations('/retake')[0].body).toMatchObject({ project_id: 'p1', confirm_execution: true, confirm_remote_provider: false });
  // The old worktree held no work of its own, so it goes without asking.
  expect(daemon.mutations('/discard-worktree')[0].body).toMatchObject({ path: '/work/kogin/.worktrees/reject-nested-asset-a1b2c3' });
  await expect(page.getByText('main changed 1 file')).toHaveCount(0);
});

test('pressing Run agents on a task whose files main changed says so before any stage runs', async ({ page, daemon }) => {
  await draftInItsOwnWorktree(page);
  // The page was loaded before main moved: it learns when the agents are asked to start.
  daemon.overtake('chg-100', ['internal/api/handlers/webui.go']);
  await page.locator('header.task-header').getByRole('button', { name: 'Investigate & plan' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('main changed 1 file of this task since it started');
  await expect(dialog).toContainText("internal/api/handlers/webui.go. This task's work is built on that file as it was: re-analyze it against main and, if the issue is still there, start it again from main. The agents don't run on it as it is.");
  await dialog.getByRole('button', { name: 'Not now' }).click();
  expect(daemon.mutations('/workflow')).toHaveLength(0);
  await expect(page.getByText('main changed 1 file of this task since it started')).toBeVisible();

  await page.locator('header.task-header').getByRole('button', { name: 'Investigate & plan' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Re-analyze & retake' }).click();
  await expect(page.getByText('Still valid: “Reject nested asset paths” starts again from main on orca/reject-nested-asset-d4e5f6. Run the agents when ready.')).toBeVisible();
  expect(daemon.mutations('/workflow')).toHaveLength(0);
  expect(daemon.mutations('/retake')).toHaveLength(1);
});

test('a run that main overtook stops before its next stage and reads out of date', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  const reason = "main changed internal/api/handlers/webui.go since this task started. This task's work is built on that file as it was: re-analyze it against main and, if the issue is still there, start it again from main.";
  const stages = session.workflow!.stages.map(stage => (stage.name === 'create' ? stage : { name: stage.name, status: stage.name === 'human_review' ? 'blocked' : 'skipped' }));
  daemon.sessions.set('chg-1', { ...session, workflow: { ...session.workflow!, status: 'stale', reason, input: undefined, stages } });
  daemon.overtake('chg-1', ['internal/api/handlers/webui.go']);
  await page.goto('/#/tasks');
  const row = page.getByRole('listitem').filter({ hasText: 'Reject nested asset paths' });
  await expect(row).toContainText('Out of date');
  await expect(row).toContainText('main changed internal/api/handlers/webui.go since this task started.');
  await row.click();
  // Told once, at the top, with the way on.
  await expect(page.getByText('main changed 1 file of this task since it started')).toHaveCount(1);
  await expect(page.getByText('Its files changed during this run')).toHaveCount(0);
  await expect(page.locator('.callout').getByRole('button', { name: 'Re-analyze & retake' })).toBeVisible();
});

test('a retaken task that is no longer valid is deleted, and its work discarded only if asked', async ({ page, daemon }) => {
  await draftInItsOwnWorktree(page);
  daemon.overtake('chg-100', ['internal/api/handlers/webui.go']);
  daemon.patchWorktree('/work/kogin/.worktrees/reject-nested-asset-a1b2c3', { dirty: true });
  daemon.retakeOutcome = 'not_valid';
  await page.reload();
  await page.getByRole('button', { name: 'Re-analyze & retake' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Re-analyze & retake' }).click();

  await expect(page.getByText('No longer valid: “Reject nested asset paths” was deleted.')).toBeVisible();
  await expect(page).toHaveURL(/#\/tasks$/);
  await expect(page.getByRole('alertdialog')).toContainText('Discard the worktree orca/reject-nested-asset-a1b2c3?');
  await page.getByRole('alertdialog').getByRole('button', { name: 'Keep it' }).click();
  expect(daemon.mutations('/discard-worktree')).toHaveLength(0);
});

test('a task made in the main checkout can only be read and deleted', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, worktree: undefined, reviewed_files: session.changes.map(change => change.path), reviewed_hash: session.hash, workflow: { ...session.workflow!, status: 'failed', reason: 'go test failed' } });
  await page.goto('/#/tasks/chg-1');
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  await expect(gate).toContainText('Made in the main checkout, before every task got its own worktree.');
  await expect(gate).toContainText('Kogin no longer changes tasks made in the main checkout.');
  await expect(gate.getByRole('button', { name: /^(Accept|Approve)/ })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /agents/ })).toHaveCount(0);
  await expect(page.getByRole('region', { name: 'Ask for a revision' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Delete task' })).toBeVisible();
});

test('line comments become the brief of the next revision', async ({ page, daemon }) => {
  await page.goto('/#/tasks/chg-1/files');
  const diff = page.getByRole('region', { name: /Diff of internal\/api\/handlers\/webui.go/ });
  await diff.getByRole('row').filter({ hasText: 'strings.Contains(name, "/")' }).hover();
  await diff.getByRole('button', { name: 'Comment on line 42' }).click();
  await diff.getByRole('textbox', { name: 'Comment on line 42' }).fill('Also reject backslashes.');
  await diff.locator('.comment-form').getByRole('button', { name: /^Comment/ }).click();
  await expect(diff.getByText('Also reject backslashes.')).toBeVisible();
  expect(daemon.mutations('/comments')[0].body).toMatchObject({ path: 'internal/api/handlers/webui.go', side: 'new', line: 42, text: 'Also reject backslashes.', revision: 3 });
  await expect(page.getByLabel('1 open comments')).toBeVisible();

  await page.getByRole('button', { name: 'Revise with 1 comment' }).click();
  await expect(page.getByText('Proposal updated. Review the new diff.')).toBeVisible();
  expect(daemon.mutations('/messages')[0].body).toMatchObject({ include_comments: true, message: '' });
  await expect(page.getByRole('button', { name: /Revise with/ })).toHaveCount(0);
});

test('a revision that takes long shows its time and can be stopped', async ({ page }) => {
  let sent = 0;
  // The agent never answers this revision: only Stop ends the wait.
  await page.route(url => url.pathname.endsWith('/changes/chg-1/messages'), () => {
    sent++;
  });
  await page.goto('/#/tasks/chg-1');
  const revise = page.getByRole('region', { name: 'Ask for a revision' });
  await revise.getByLabel('Revision request').fill('Also reject backslashes.');
  await revise.getByRole('button', { name: /^Revise/ }).click();
  const progress = revise.getByRole('status');
  await expect(progress).toContainText(/Agent working · 0:0\d/);
  await expect(revise.getByRole('button', { name: /^Revise/ })).toBeDisabled();

  await progress.getByRole('button', { name: 'Stop' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Stop revision' }).click();
  await expect(page.getByText('Revision stopped.')).toBeVisible();
  await expect(progress).toHaveCount(0);
  await expect(revise.getByLabel('Revision request')).toHaveValue('Also reject backslashes.');
  expect(sent).toBe(1);
});

test('a task revised in the conversation runs the agents again on that version', async ({ page, daemon }) => {
  await page.goto('/#/tasks/chg-1');
  const header = page.locator('header.task-header');
  // A run is always on offer, beside the review step.
  await expect(header.getByRole('button', { name: 'Review files' })).toBeVisible();
  await expect(header.getByRole('button', { name: 'Run agents again' })).toBeEnabled();

  const revise = page.getByRole('region', { name: 'Ask for a revision' });
  await revise.getByLabel('Revision request').fill('Also reject backslashes.');
  await revise.getByRole('button', { name: /^Revise/ }).click();
  await expect(page.getByText('Proposal updated. Review the new diff.')).toBeVisible();
  const run = page.getByRole('region', { name: 'Agent run' });
  await expect(run.getByText('The proposal changed after this run')).toBeVisible();
  // A revision after the run is not a failure, and the old run no longer counts.
  await expect(header.getByText('Revised')).toBeVisible();
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  await expect(gate.getByText('Not run on this version')).toBeVisible();
  await expect(gate.getByText('Run after the agents')).toBeVisible();

  await run.getByRole('button', { name: 'Run agents again' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByRole('heading', { name: 'Run the agents again?' })).toBeVisible();
  await expect(dialog.getByRole('radio', { name: /Check and review this version/ })).toBeChecked();
  await dialog.getByRole('button', { name: 'Run agents' }).click();
  await expect(header.getByRole('button', { name: 'Stop agents' })).toBeVisible();
  expect(daemon.mutations('/workflow')[0].body).toMatchObject({ resume_from: 'checks', revision: 4, hash: 'sha256:9f2c1a7be4-r4', auto_repair: true });
  // The kept tests are only run: the test stage says so.
  await expect(run.getByText('Run the checks')).toBeVisible();
});

test('a run paused at a plan limit says when it can continue and resumes where it stopped', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', pausedTask());
  await page.goto('/#/tasks/chg-1');
  const run = page.getByRole('region', { name: 'Agent run' });
  await expect(run.getByText(/^Paused until the Claude plan's limit resets (at|on) /)).toBeVisible();
  await expect(run.getByText('Nothing was lost: resume when the limit resets').first()).toBeVisible();
  await expect(page.locator('header.task-header').getByText('Paused')).toBeVisible();

  await run.getByRole('button', { name: 'Resume agents' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByRole('heading', { name: 'Resume the agents?' })).toBeVisible();
  await expect(dialog.getByRole('radio', { name: /Continue the review/ })).toBeChecked();
  await dialog.getByRole('button', { name: 'Resume agents' }).click();
  await expect(page.locator('header.task-header').getByRole('button', { name: 'Stop agents' })).toBeVisible();
  expect(daemon.mutations('/workflow')[0].body).toMatchObject({ resume_from: 'review' });
});

test('a run paused at a plan limit can go on now with another model', async ({ page, daemon }) => {
  daemon.serve('/api/projects/current/agent-settings', () => agentSettings(1, { creation: 'bug', testing: 'code', review: 'claude:opus' }, null));
  const task = pausedTask();
  daemon.sessions.set('chg-1', { ...task, workflow: { ...task.workflow!, stages: task.workflow!.stages.map(stage => (stage.name === 'review' ? { ...stage, model: stageModel('claude:opus') } : stage)) } });
  await page.goto('/#/tasks/chg-1');
  await page.getByRole('region', { name: 'Agent run' }).getByRole('button', { name: 'Resume agents' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByText('The Claude plan reached its usage limit: choose another model to continue now.')).toBeVisible();
  // Continuing the review keeps the change and its tests with the models they ran with.
  await expect(dialog.getByLabel('Write the change', { exact: true })).toBeDisabled();
  await expect(dialog.getByLabel('Write and run tests', { exact: true })).toBeDisabled();
  await dialog.getByLabel('Specialist review', { exact: true }).selectOption('agy:gemini-3.8-flash-medium');
  await expect(dialog.getByText('The Claude plan reached its usage limit: choose another model to continue now.')).toBeHidden();
  await dialog.getByRole('button', { name: 'Resume agents' }).click();
  await expect(page.locator('header.task-header').getByRole('button', { name: 'Stop agents' })).toBeVisible();
  const body = daemon.mutations('/workflow')[0].body as { models: Record<string, string>; confirmed_profiles: string[] };
  expect(body).toMatchObject({ resume_from: 'review', models: { create: 'bug', test: 'code', review: 'agy:gemini-3.8-flash-medium' } });
  expect(body.confirmed_profiles).toContain('agy:gemini-3.8-flash-medium');
});

test('a changed creation model makes the agents write the change again', async ({ page, daemon }) => {
  daemon.serve('/api/projects/current/agent-settings', () => agentSettings(1, { creation: 'code', testing: 'code', review: 'analyze' }, null));
  await page.goto('/#/tasks/chg-1');
  await page.locator('header.task-header').getByRole('button', { name: 'Run agents again' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByRole('radio', { name: /Check and review this version/ })).toBeDisabled();
  await expect(dialog.getByRole('radio', { name: /Write new tests/ })).toBeDisabled();
  await expect(dialog.getByText('The creation model changed since the last run, so the agents write the change again.').first()).toBeVisible();
  await expect(dialog.getByRole('radio', { name: /Write the change again/ })).toBeChecked();
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  expect(daemon.mutations('/workflow')).toEqual([]);
});

test('line comments go to the agents only with a start that writes code', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, comments: [{ id: 'cm-1', path: 'internal/api/handlers/webui.go', side: 'new', line: 42, excerpt: 'if name == "" || strings.Contains(name, "/") {', text: 'Prefer path.Clean.', hash: session.hash, created_at: session.updated_at }] });
  await page.goto('/#/tasks/chg-1');
  await page.locator('header.task-header').getByRole('button', { name: 'Run agents again' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('Your 1 line comment stays open: this start writes no code.');
  await dialog.getByRole('radio', { name: /Write new tests/ }).check();
  await expect(dialog).toContainText('Your 1 line comment goes to the agents as a repair brief.');
  await dialog.getByRole('button', { name: 'Run agents' }).click();
  await expect(page.locator('header.task-header').getByRole('button', { name: 'Stop agents' })).toBeVisible();
  expect(daemon.mutations('/workflow')[0].body).toMatchObject({ resume_from: 'test', include_comments: true });
});

test('a comment can be deleted before it is sent', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  daemon.sessions.set('chg-1', { ...session, comments: [{ id: 'cm-1', path: 'internal/api/handlers/webui.go', side: 'new', line: 42, excerpt: 'if name == "" || strings.Contains(name, "/") {', text: 'Prefer path.Clean.', hash: session.hash, created_at: session.updated_at }] });
  await page.goto('/#/tasks/chg-1/files');
  await expect(page.getByText('Prefer path.Clean.')).toBeVisible();
  await page.getByRole('button', { name: 'Delete comment' }).click();
  await expect(page.getByText('Prefer path.Clean.')).toBeHidden();
  expect(daemon.mutations('/comments/delete')[0].body).toMatchObject({ id: 'cm-1' });
});

test('a page from an earlier daemon launch is told to reload', async ({ page, daemon }) => {
  daemon.expectedConsole.push(/status of 401/);
  await page.goto('/#/tasks/chg-1/files');
  await page.evaluate(() => document.querySelector('meta[name="kogin-write-token"]')?.setAttribute('content', 'previous-launch'));
  await page.keyboard.press('v');
  await expect(page.getByText("Reload the Web UI to get this daemon's write token.")).toBeVisible();
  expect(daemon.sessions.get('chg-1')!.reviewed_files).toEqual([]);
});

test('a task in its own worktree shows new, deleted and moved files and is accepted as a commit', async ({ page, daemon }) => {
  const session = daemon.sessions.get('chg-1')!;
  const changes = [
    ...session.changes.map(change => (change.path.endsWith('mime.go') ? { ...change, status: 'added' as const } : change)),
    { path: 'internal/api/handlers/legacy.go', status: 'deleted' as const, content: '', hash: 'h-legacy', diff: { lines: [{ kind: 'removed', old_line: 1, text: 'package handlers' }] } },
    { path: 'internal/api/handlers/assets.go', status: 'renamed' as const, old_path: 'internal/api/handlers/static.go', content: 'package handlers\n', hash: 'h-assets', diff: { old_path: 'internal/api/handlers/static.go', new_path: 'internal/api/handlers/assets.go', lines: [{ kind: 'context', old_line: 1, new_line: 1, text: 'package handlers' }] } },
  ];
  daemon.sessions.set('chg-1', { ...session, changes, reviewed_files: changes.map(change => change.path), reviewed_hash: session.hash, worktree: { base: '41030b7e', branch: 'orca/reject-nested-a1b2c3' } });

  await page.goto('/#/tasks/chg-1/files');
  const files = page.getByRole('complementary', { name: 'Changed files' });
  const diff = page.locator('.fc-diff-head');
  await files.getByRole('button', { name: /legacy\.go/ }).click();
  await expect(diff).toContainText('deleted');
  await files.getByRole('button', { name: /assets\.go/ }).click();
  await expect(diff).toContainText('renamed from internal/api/handlers/static.go');
  await files.getByRole('button', { name: /mime\.go/ }).click();
  await expect(diff).toContainText('new');

  await page.getByRole('tab', { name: /conversation/i }).click();
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  await expect(gate).toContainText('Ready to accept?');
  await expect(gate.getByRole('button', { name: /apply/i })).toHaveCount(0);
  await gate.getByRole('button', { name: 'Accept & commit' }).click();
  const dialog = page.getByRole('dialog', { name: 'Accept and commit?' });
  await expect(dialog).toContainText('on orca/reject-nested-a1b2c3');
  await dialog.getByRole('button', { name: 'Accept & commit' }).click();
  await expect(page.getByText('Accepted and committed on orca/reject-nested-a1b2c3.')).toBeVisible();
  expect(daemon.mutations('/apply')[0].body).toMatchObject({ confirm: true, revision: 3 });
  expect(daemon.mutations('/apply')[0].body).not.toHaveProperty('head');
  await expect(gate).toContainText('Accepted');
  await expect(gate.getByRole('button', { name: 'Merge from Branches' })).toBeVisible();
});

test('a finding is a few words with a link to its line in the diff', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', rejectedTask());
  await page.goto('/#/tasks/chg-1');
  const finding = page.locator('.review-item', { hasText: 'MIME table belongs in its own package' });
  await expect(finding).toBeVisible();
  // The detail waits behind "Why".
  await expect(finding.getByText('Move the MIME table into its own package.')).toBeHidden();
  await finding.getByText('Why').click();
  await expect(finding.getByText('Move the MIME table into its own package.')).toBeVisible();

  await finding.getByRole('button', { name: 'webui.go:42' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-1\/files\?.*file=internal%2Fapi%2Fhandlers%2Fwebui\.go.*line=42/);
  const diff = page.getByRole('region', { name: /Diff of internal\/api\/handlers\/webui.go/ });
  await expect(diff.locator('tr.is-focus')).toContainText('strings.Contains(name, "/")');
});

test('the testing agent\'s follow-ups become tasks and its dispute decisions are shown', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', {
    ...reviewTask(),
    decided_tests: ['internal/api/handlers/webui_test.go'],
    follow_ups: [{ title: 'Cache asset reads', detail: 'Assets are read from disk on every request; caching needs the embed package.', path: 'internal/api/assets.go' }],
  });
  await page.goto('/#/tasks/chg-1');
  const decisions = page.getByRole('list', { name: 'Testing decisions' });
  await expect(decisions).toContainText('the testing agent decided: internal/api/handlers/webui_test.go');
  await expect(decisions).toContainText('Assets are read from disk on every request');
  await decisions.getByRole('button', { name: 'New task' }).click();
  const composer = page.getByRole('dialog', { name: 'New task' });
  await expect(composer.getByLabel('Task title')).toHaveValue('Cache asset reads');
  await expect(composer.getByLabel('Description')).toHaveValue('Assets are read from disk on every request; caching needs the embed package.');
});

test('a task waiting for another changing the same code says so and links to it', async ({ page, daemon }) => {
  const task = reviewTask();
  daemon.sessions.set('chg-1', {
    ...task,
    workflow: {
      ...task.workflow!,
      status: 'waiting',
      reason: 'Waiting for “Rework the vault key”, which is changing internal/api/assets.go. This task continues by itself on the updated code once that task is merged or deleted.',
      waiting: { task_id: 'chg-2', title: 'Rework the vault key', worktree_path: '/work/kogin/.worktrees/vault', files: ['internal/api/assets.go'] },
      review: undefined,
    },
  });
  await page.goto('/#/tasks/chg-1');
  const run = page.getByRole('region', { name: 'Agent run' });
  await expect(run.getByText('Waiting for “Rework the vault key”', { exact: true })).toBeVisible();
  await expect(run.getByText(/continues by itself on the updated code/)).toBeVisible();
  await expect(page.locator('header.task-header').getByText('Waiting')).toBeVisible();
  // The other task is in a worktree this fake daemon does not serve.
  daemon.expectedConsole.push(/status of 404/);
  await run.getByRole('button', { name: 'Open that task' }).click();
  await expect(page).toHaveURL(/#\/tasks\/chg-2\?wt=/);
});

test('a task with nothing to fix is closed, dismissing the issues that do not hold', async ({ page, daemon }) => {
  const task = reviewTask();
  const issue = (id: string, title: string, step: Record<string, string>) => ({ finding_id: id, path: 'internal/api/handlers/webui.go', title, detail: title, ...step });
  daemon.sessions.set('chg-1', {
    ...task,
    changes: [],
    issues: [issue('f1', 'Nested asset paths are served', { status: 'does_not_hold', reason: 'ServeAsset rejects nested names.' }), issue('f2', 'Missing assets return 500', { status: 'not_fixed', reason: 'Checks still failed after one repair.' })],
    workflow: { ...task.workflow!, status: 'nothing_to_fix', reason: 'Nothing to fix: 1 of the 2 issues do not hold, and 1 could not be fixed (open again in Issues).', review: undefined },
  });
  await page.goto('/#/tasks/chg-1');
  const run = page.getByRole('region', { name: 'Agent run' });
  await expect(run.getByText('Nothing to fix', { exact: true })).toBeVisible();
  await run.getByRole('button', { name: 'Dismiss the issue' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByText('It is dismissed; you can reopen it in Issues. The task is deleted; it changed no files.')).toBeVisible();
  await expect(dialog.getByText('Nested asset paths are served')).toBeVisible();
  await dialog.getByRole('button', { name: 'Dismiss the issue' }).click();
  await expect(page).toHaveURL(/#\/tasks$/);
  expect(daemon.mutations('/close')[0].body).toMatchObject({ revision: task.revision, hash: task.hash });
});

test('a plan that finds nothing to fix offers to dismiss the issue instead of running agents', async ({ page, daemon }) => {
  const task = planReadyTask();
  daemon.sessions.set('chg-1', {
    ...task,
    title: 'Fix: Global master key left set',
    finding_targets: [{ project_id: 'p1', kind: 'semantic', id: 'f1', path: 'internal/api/handlers/webui.go' }],
    plan: { ...task.plan!, problem: 'Reported: the key is left set. It is not: the cleanup clears it.', approach: 'Nothing to change.', touches: [], issue_verdicts: [{ finding_id: 'f1', verdict: 'does_not_hold', note: 'withTempHome clears the key when each subtest ends.' }] },
  });
  await page.goto('/#/tasks/chg-1');
  const plan = page.getByRole('region', { name: 'Proposed plan' });
  await expect(plan.getByText('Nothing to fix', { exact: true })).toBeVisible();
  await expect(plan.getByText('withTempHome clears the key when each subtest ends.')).toBeVisible();
  // Nothing offers to run agents: the header and the card dismiss, or redirect.
  const header = page.locator('header.task-header');
  await expect(header.getByRole('button', { name: 'Dismiss the issue' })).toBeVisible();
  await expect(page.getByRole('button', { name: /Approve & run agents|Start agents|Investigate & plan/ })).toHaveCount(0);
  await expect(page.getByRole('complementary', { name: 'Review gate' }).getByText('Found nothing to fix', { exact: true })).toBeVisible();
  await plan.getByRole('button', { name: 'I think it’s real…' }).click();
  await expect(plan.getByPlaceholder('Why do you think the issue is real? The investigation runs again with this note.')).toBeVisible();
  await plan.getByRole('button', { name: 'Cancel' }).click();
  await plan.getByRole('button', { name: 'Dismiss the issue' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByText('Global master key left set')).toBeVisible();
  await dialog.getByRole('button', { name: 'Dismiss the issue' }).click();
  await expect(page).toHaveURL(/#\/tasks$/);
  expect(daemon.mutations('/close')[0].body).toMatchObject({ revision: task.revision, hash: task.hash });
});

test('a task that fixes its issues one at a time shows each step', async ({ page, daemon }) => {
  const issue = (id: string, title: string, step: Record<string, string>) => ({ finding_id: id, path: 'internal/api/handlers/webui.go', title, detail: title, ...step });
  daemon.sessions.set('chg-1', {
    ...reviewTask(),
    issues: [
      issue('f1', 'Nested asset paths are served', { status: 'fixed', commit: '3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d' }),
      issue('f2', 'Missing assets return 500', { status: 'not_fixed', reason: 'The coding agent changed nothing: the handler already returns 404.' }),
      issue('f3', 'Asset reads are not cached', { status: 'pending' }),
      issue('f4', 'Asset names are not escaped', { status: 'does_not_hold', reason: 'ServeAsset passes names through path.Clean before use.' }),
      issue('f5', 'Nested names are accepted', { status: 'fixed', commit: '3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d', reason: 'Already fixed by an earlier step: the first step rejects nested names.' }),
    ],
    scope_grants: [{ path: 'internal/api/assets.go', reason: 'The asset lookup the handler calls lives there.', issue: 'f1' }],
  });
  await page.goto('/#/tasks/chg-1');
  const steps = page.getByRole('list', { name: 'Issues, one at a time' });
  await expect(steps.getByRole('listitem')).toHaveCount(5);
  await expect(steps.getByRole('listitem').nth(3)).toContainText('Does not hold');
  await expect(steps.getByRole('listitem').nth(3)).toContainText('ServeAsset passes names through path.Clean before use. Merging the task dismisses it; you can reopen it in Issues.');
  await expect(steps.getByRole('listitem').nth(4)).toContainText('Already fixed by an earlier step: the first step rejects nested names.');
  const added = page.getByRole('list', { name: 'Added to the fix' });
  await expect(added).toContainText('internal/api/assets.go');
  await expect(added).toContainText('The asset lookup the handler calls lives there.');
  await expect(steps.getByRole('listitem').nth(0)).toContainText('1. Nested asset paths are served');
  await expect(steps.getByRole('listitem').nth(0)).toContainText('3c4d5e6');
  await expect(steps.getByRole('listitem').nth(0)).toContainText('Fixed');
  await expect(steps.getByRole('listitem').nth(1)).toContainText('Not fixed');
  await expect(steps.getByRole('listitem').nth(1)).toContainText('The coding agent changed nothing: the handler already returns 404. It is open again in Issues.');
  await expect(steps.getByRole('listitem').nth(2)).toContainText('Waiting');
});

test('an investigated plan waits for approval, and approving it starts the agents', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', planReadyTask());
  await page.goto('/#/tasks/chg-1');
  const plan = page.getByRole('region', { name: 'Proposed plan' });
  await expect(plan.getByText('Needs your decision')).toBeVisible();
  // Problem and approach read at a glance; the reasoning folds behind "Why this plan".
  await expect(plan.getByText('Nested asset paths reach the embedded read and return 500 instead of 404.')).toBeVisible();
  await expect(plan.getByText('ServeAsset trims the name but never rejects a slash, so fs.ReadFile gets a nested path and fails.')).toBeHidden();
  await plan.getByText('Why this plan').click();
  await expect(plan.getByText('ServeAsset trims the name but never rejects a slash, so fs.ReadFile gets a nested path and fails.')).toBeVisible();
  await expect(plan.getByRole('button', { name: 'webui.go:41–42' })).toBeVisible();
  await expect(plan.getByText('— The bundle has no nested files; allowing them widens what the handler serves.')).toBeVisible();
  await expect(plan.getByText('A client that relied on the 500 now sees 404.')).toBeVisible();
  // The investigation is its own timeline entry, with its model and tool calls.
  const run = page.getByRole('region', { name: 'Agent run' });
  await expect(run.getByText('Investigate & plan')).toBeVisible();
  await expect(run.getByText('Worked with tools: 9 calls in 11 turns (read_file 5, search 3, plan 1)')).toBeVisible();
  await expect(page.locator('header.task-header').getByText('Plan ready')).toBeVisible();
  // The page is about the decision: the header's next step approves the plan,
  // the review gate puts the plan first, and nothing offers to generate code.
  const header = page.locator('header.task-header');
  await expect(header.getByRole('button', { name: 'Approve & run agents' })).toBeVisible();
  await expect(header.getByRole('button', { name: /Start agents|Investigate & plan/ })).toHaveCount(0);
  const gate = page.getByRole('complementary', { name: 'Review gate' });
  await expect(gate.getByText('Needs your approval')).toBeVisible();
  await expect(gate.getByText('After you approve the plan')).toBeVisible();
  await expect(gate).toContainText('The investigation proposed a plan. Approve it to start the agents, or redirect it with a note.');
  await expect(page.getByRole('region', { name: 'Ask for a revision' })).toHaveCount(0);

  await header.getByRole('button', { name: 'Approve & run agents' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByRole('heading', { name: 'Approve the plan and start the agents?' })).toBeVisible();
  await expect(dialog.getByText('Approves this exact plan.')).toBeVisible();
  await dialog.getByRole('button', { name: 'Approve & run agents' }).click();
  await expect(page.locator('header.task-header').getByRole('button', { name: 'Stop agents' })).toBeVisible();
  expect(daemon.mutations('/workflow')[0].body).toMatchObject({ plan_hash: 'plan-hash-1' });
  // The approved run keeps the investigation on its timeline.
  await expect(run.getByText('Investigate & plan')).toBeVisible();
});

test('redirecting the plan runs the investigation again with the note', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', planReadyTask());
  await page.goto('/#/tasks/chg-1');
  const plan = page.getByRole('region', { name: 'Proposed plan' });
  await plan.getByRole('button', { name: 'Redirect…' }).click();
  await plan.getByLabel('Redirect note').fill('Keep the 500 for nested paths; only missing assets become 404.');
  await plan.getByRole('button', { name: 'Re-investigate' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog.getByRole('heading', { name: 'Redirect the plan?' })).toBeVisible();
  await expect(dialog.getByText('The investigation runs again with your note; the revised plan waits for you.')).toBeVisible();
  await dialog.getByRole('button', { name: 'Re-investigate' }).click();
  const body = daemon.mutations('/workflow')[0].body as Record<string, unknown>;
  expect(body.plan_redirect).toBe('Keep the 500 for nested paths; only missing assets become 404.');
  expect(body.plan_hash).toBeUndefined();
});

test('a guided review walks the hunks in causal order with the plan threading through', async ({ page, daemon }) => {
  daemon.sessions.set('chg-1', guidedReviewTask());
  await page.goto('/#/tasks/chg-1/files');
  const pane = page.getByRole('complementary', { name: 'This hunk' });
  // The file list says where each file comes in the story.
  const tree = page.getByRole('complementary', { name: 'Changed files' });
  await expect(tree.getByRole('button', { name: /mime\.go/ })).toContainText('step 2');
  await expect(tree.getByRole('button', { name: /webui_test\.go/ })).toContainText('step 3');
  // Step 1 is the fix, wherever it sits in the file list, with the plan's problem above.
  await expect(pane).toContainText('Step 1 of 3');
  await expect(pane).toContainText('Fix');
  await expect(pane).toContainText('Fixing: Nested asset paths return 500 instead of 404.');
  await expect(page.locator('.fc-diff-head')).toContainText('webui.go');
  // The next step is the supporting change in another file, then the proof.
  await page.keyboard.press('j');
  await expect(pane).toContainText('Step 2 of 3');
  await expect(pane).toContainText('Supports the fix');
  await expect(page.locator('.fc-diff-head')).toContainText('mime.go');
  await page.keyboard.press('j');
  await expect(pane).toContainText('Step 3 of 3');
  await expect(pane).toContainText('Proof');
  await expect(page.locator('.fc-diff-head')).toContainText('webui_test.go');
});
