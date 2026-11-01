// The analysis dashboard: progress, why stages failed, results per section,
// every file's stages, and the run controls.

import { expect, test } from './support/daemon';
import { analysisRun } from './support/fixtures';

test('the dashboard explains a failed run and retries only what failed', async ({ page, daemon }) => {
  daemon.serveRun(analysisRun());
  await page.goto('/#/');
  await page.keyboard.press('g');
  await page.keyboard.press('a');
  await expect(page).toHaveURL(/#\/analysis/);
  await expect(page.getByRole('heading', { name: 'Analysis' })).toBeVisible();

  const progress = page.getByRole('region', { name: 'Progress' });
  await expect(progress).toContainText('16 of 16 stages finished');
  await expect(progress).toContainText('6stages failed');

  const failures = page.getByRole('region', { name: 'Why stages failed' });
  await expect(failures).toContainText('The model provider could not be reached.');
  await expect(failures).toContainText('Bugs, Performance, Security review · 2 files');
  await expect(failures.getByRole('button', { name: 'Check models' })).toBeVisible();

  await expect(page.getByRole('region', { name: 'Models used' })).toContainText('local-bug-model');
  await expect(page.getByRole('region', { name: 'Bugs results' })).toContainText('2 findings');
  const checks = page.getByRole('region', { name: 'Checks' });
  await expect(checks).toContainText('3 issues');
  await expect(checks.getByRole('listitem').filter({ hasText: 'go test ./...' })).toContainText('2 issuesfailed');

  await failures.getByRole('button', { name: 'Show failed files' }).click();
  const files = page.getByRole('region', { name: 'Files' });
  await expect(files.getByRole('row')).toHaveCount(3);
  await expect(files).toContainText('internal/vault/crypto.go');
  await expect(files).not.toContainText('internal/api/handlers/webui.go');

  await page.getByRole('region', { name: 'Bugs results' }).getByRole('button', { name: '2 findings' }).click();
  await expect(page).toHaveURL(/#\/issues\?category=bugs/);
  await page.goBack();

  await page.getByRole('button', { name: 'Retry failed' }).click();
  const dialog = page.getByRole('alertdialog');
  await expect(dialog).toContainText('Retries the failed, stale and canceled stages of 2 files');
  await expect(dialog).toContainText('The other 2 files keep their saved results.');
  await expect(dialog).toContainText('Then checks the parser diagnostics and the AGENTS.md code budgets of every file');
  await expect(dialog.getByRole('listitem').filter({ hasText: 'tests: web' })).toContainText('npm test');
  await dialog.getByRole('button', { name: 'Analyze' }).click();
  await expect(page.getByText('Analysis started.')).toBeVisible();
  const [preview] = daemon.mutations('/analysis/preview');
  expect(preview.body).toMatchObject({ retry_stale_failed: true, refresh: false, scope: 'project' });
  expect(daemon.mutations('/analysis/run')[0].body).toMatchObject({ preview_id: 'preview-1', retry_stale_failed: true });
  await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
});

test('the analysis offers one next step and the other ways to run under More', async ({ page, daemon }) => {
  const run = analysisRun('completed');
  run.files = (run.files ?? []).map(file => ({ ...file, stages: file.stages.map(stage => (stage.status === 'failed' ? { ...stage, status: 'completed_empty', reason: undefined, finding_count: 0 } : stage)) }));
  daemon.serveRun(run);
  await page.goto('/#/analysis');
  const head = page.locator('.page-head');
  await expect(head.getByRole('button', { name: 'Analyze changes' })).toBeVisible();
  await expect(head.getByRole('button', { name: /Retry/ })).toHaveCount(0);
  await head.getByRole('button', { name: 'More ways to analyze' }).click();
  const menu = page.getByRole('menu');
  await expect(menu.getByRole('menuitem')).toHaveCount(1);
  await menu.getByRole('menuitem', { name: /Re-analyze every file/ }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Analyze' }).click();
  expect(daemon.mutations('/analysis/preview')[0].body).toMatchObject({ refresh: true });
});

test('after failures, retrying them is the next step and analyzing changes moves under More', async ({ page, daemon }) => {
  daemon.serveRun(analysisRun());
  await page.goto('/#/analysis');
  const head = page.locator('.page-head');
  await expect(head.getByRole('button', { name: 'Retry failed' })).toBeVisible();
  await expect(head.getByRole('button', { name: 'Analyze changes' })).toHaveCount(0);
  await head.getByRole('button', { name: 'More ways to analyze' }).click();
  await expect(page.getByRole('menu').getByRole('menuitem')).toHaveText([/Analyze changes/, /Re-analyze every file/]);
});

test('when the files changed since a run with failures, analyzing the changes is the next step', async ({ page, daemon }) => {
  daemon.serveRun({ ...analysisRun('stale'), reason: 'Files, settings or models changed since this run. Analyze changes to bring the results up to date.' });
  await page.goto('/#/analysis');
  await expect(page.getByText('Files, settings or models changed since this run.')).toBeVisible();
  const head = page.locator('.page-head');
  await expect(head.getByRole('button', { name: 'Analyze changes' })).toBeVisible();
  await head.getByRole('button', { name: 'More ways to analyze' }).click();
  await expect(page.getByRole('menu').getByRole('menuitem')).toHaveText([/Retry failed only/, /Re-analyze every file/]);
});

test('a retry keeps the rest of the project on the dashboard', async ({ page, daemon }) => {
  const run = analysisRun('completed');
  run.plan = { ...run.plan, retry_stale_failed: true, files: (run.files ?? []).map((file, i) => ({ path: file.path, retained: i > 1 })) };
  daemon.serveRun(run);
  await page.goto('/#/analysis');
  const progress = page.getByRole('region', { name: 'Progress' });
  await expect(progress).toContainText('This run analyzed 2 files again; the other 2 files keep their saved results');
  await expect(page.getByRole('region', { name: 'Files' }).getByRole('row')).toHaveCount(5);
});

test('a canceled retry can be retried, and the saved results stay done', async ({ page, daemon }) => {
  const run = analysisRun('canceled');
  run.files = (run.files ?? []).map(file => ({ ...file, stages: file.stages.map(stage => (stage.status === 'failed' ? { ...stage, status: 'canceled', reason: 'Analysis canceled by the user.' } : stage)) }));
  daemon.serveRun(run);
  await page.goto('/#/analysis');
  await expect(page.getByRole('region', { name: 'Progress' })).toContainText('0stages failed');
  await page.getByRole('button', { name: 'Retry canceled' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Analyze' }).click();
  expect(daemon.mutations('/analysis/preview')[0].body).toMatchObject({ retry_stale_failed: true });
});

test('a running analysis can be paused and resumed with a fresh preview', async ({ page, daemon }) => {
  daemon.serveRun(analysisRun('running'));
  await page.goto('/#/analysis');
  await expect(page.getByRole('navigation', { name: 'Workspace' }).getByRole('link', { name: /Analysis/ })).toContainText('1');
  await expect(page.getByRole('contentinfo', { name: 'Status' }).getByRole('button', { name: /Analyzing \d+%/ })).toBeVisible();
  await page.getByRole('button', { name: 'Pause' }).click();
  await expect(page.getByRole('button', { name: 'Resume' })).toBeVisible();
  expect(daemon.mutations('/analysis/run/control')[0].body).toMatchObject({ action: 'pause', identity: { id: 'analysis-1' } });

  await page.getByRole('button', { name: 'Resume' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Resume' }).click();
  await expect(page.getByText('Analysis resumed.')).toBeVisible();
  expect(daemon.mutations('/analysis/preview')[0].body).toMatchObject({ resume_run: { id: 'analysis-1' }, limits: { budget_seconds: 1800 } });
  expect(daemon.mutations('/analysis/run/control')[1].body).toMatchObject({ action: 'resume', preview_id: 'preview-1' });
});

test('the code inspector says what the open file does and how analysis went for it', async ({ page, daemon }) => {
  daemon.serveRun(analysisRun());
  await page.goto('/#/code?file=internal%2Fapi%2Fhandlers%2Fwebui.go');
  const about = page.getByRole('region', { name: 'About this file' });
  await expect(about).toContainText('Serves the embedded Web UI bundle.');
  await expect(about).not.toContainText('Reads the embedded files');
  await about.getByRole('button', { name: 'More from the summary' }).click();
  await expect(about).toContainText('Reads the embedded files');
  await expect(about.getByRole('button', { name: 'internal/api' })).toBeVisible();
  expect(daemon.requests.filter(r => r.method === 'POST' && r.path.includes('/files/'))).toEqual([]);
  const stages = page.getByLabel('Analysis of this file');
  await expect(stages).toContainText('Bugs');
  await expect(stages).toContainText('2 findings');
  await stages.getByRole('button', { name: 'Dashboard' }).click();
  await expect(page).toHaveURL(/#\/analysis/);
});
