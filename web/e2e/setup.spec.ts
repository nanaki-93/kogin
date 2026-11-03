// First-run project setup: the wizard opens for a project without its own
// configuration, saves models, agents and budget, then checks and exclusions.

import { expect, test } from './support/daemon';

test('a project without configuration opens setup and saves it step by step', async ({ page, daemon }) => {
  daemon.serveSetup('needed');
  await page.goto('/#/');
  await expect(page).toHaveURL(/#\/setup/);
  await expect(page.getByRole('heading', { name: 'Set up kogin' })).toBeVisible();

  const bug = page.getByRole('region', { name: 'Bug model' });
  await bug.getByLabel('Provider').selectOption('agy');
  await expect(bug.getByLabel('API address')).toHaveCount(0);
  await bug.getByLabel('Model').fill('gemini-3.8-pro-high');
  await bug.getByLabel('Reasoning effort').selectOption('high');
  await bug.getByRole('button', { name: 'Test connection' }).click();
  await expect(bug).toContainText('Connected to gemini-3.8-pro-high');

  const analyze = page.getByRole('region', { name: 'Analyze model' });
  await expect(analyze.getByLabel('API key (optional)')).toHaveAttribute('placeholder', '•••••••• saved');
  await page.getByRole('button', { name: 'Next' }).click();

  await page.getByLabel('Reviews').selectOption('code');
  await page.getByLabel('Model calls').fill('30');
  await page.getByLabel('Analysis request (seconds)').fill('600');
  await page.getByRole('button', { name: 'Save and continue' }).click();
  await expect(page.getByText('Project configuration saved.')).toBeVisible();
  const [saved] = daemon.mutations('/setup');
  expect(saved.body).toMatchObject({
    models: { bug: { provider: 'agy', model: 'gemini-3.8-pro-high', reasoning_effort: 'high' }, analyze: { model: 'gemini-2.5-pro', api_key_set: true } },
    roles: { review: 'code' },
    limits: { model_calls: 30 },
    timeouts: { analysis_seconds: 600, workflow_model_seconds: 900 },
  });
  expect((saved.body!.models as Record<string, { api_base_url?: string }>).bug.api_base_url).toBeUndefined();

  await expect(page.getByRole('region', { name: 'Checks' })).toContainText('go test ./...');
  await page.getByRole('button', { name: 'Next' }).click();

  const reads = page.getByRole('region', { name: 'What analysis reads' });
  await expect(reads).toContainText('usually skipped');
  await reads.getByRole('checkbox', { name: /vendor\// }).uncheck();
  await expect(reads).toContainText('6 files included');
  await page.getByRole('button', { name: 'Finish', exact: true }).click();
  await expect(page).toHaveURL(/#\/$/);
  expect(daemon.mutations('/analysis/selection')[0].body).toMatchObject({ selection_id: 'sel-0', excluded_paths: ['vendor/lib/a.go', 'vendor/lib/b.go'] });
});

test('setup can be skipped and is not offered again', async ({ page, daemon }) => {
  daemon.serveSetup('needed');
  await page.goto('/#/tasks');
  await expect(page).toHaveURL(/#\/setup/);
  await page.getByRole('button', { name: 'Skip and use the daemon’s settings' }).click();
  await expect(page).toHaveURL(/#\/$/);
  expect(daemon.mutations('/setup/skip')).toHaveLength(1);
  await expect(page.getByText('Set up this project')).toHaveCount(0);
  await page.goto('/#/settings/project');
  const card = page.getByRole('region', { name: 'Project configuration' });
  await expect(card).toContainText('Daemon configuration');
  await card.getByRole('button', { name: 'Run setup' }).click();
  await expect(page).toHaveURL(/#\/setup/);
});

test('out-of-date agent settings are explained and reset, by setup or by hand', async ({ page, daemon }) => {
  daemon.staleSettings();
  await page.goto('/#/');
  const notice = page.getByText('Agent settings are out of date');
  await expect(notice).toBeVisible();
  await page.goto('/#/setup?step=agents');
  await expect(page.getByText('Saving setup replaces them')).toBeVisible();
  await page.getByRole('button', { name: 'Save and continue' }).click();
  await expect(page.getByText('Agent overrides from Settings → Models & agents were replaced by these settings.')).toBeVisible();
  await expect(page.getByText('Saving setup replaces them')).toHaveCount(0);

  daemon.staleSettings();
  await page.goto('/#/analysis');
  await page.reload();
  await page.getByRole('button', { name: 'Reset' }).click();
  await expect(page.getByText('Agents follow the configuration again.')).toBeVisible();
  expect(daemon.mutations('/agent-settings/control').at(-1)!.body).toMatchObject({ action: 'reset', version: 1 });
  await expect(page.getByText('Agent settings are out of date')).toHaveCount(0);
});

test('a configured project changes its models from Models & agents', async ({ page, daemon }) => {
  daemon.serveSetup('configured');
  await page.goto('/#/settings/models');
  await expect(page.getByText('Model scopes come from this project’s')).toBeVisible();
  await page.getByRole('button', { name: 'Change models' }).click();
  await expect(page).toHaveURL(/#\/setup/);
  await expect(page.getByRole('region', { name: 'Bug model' })).toBeVisible();
});

test('time limits are changed from Models & agents without rerunning setup', async ({ page, daemon }) => {
  daemon.serveSetup('configured');
  await page.goto('/#/settings/models');
  const limits = page.getByRole('region', { name: 'Time limits' });
  const analysis = limits.getByLabel('Analysis request (seconds)');
  await expect(analysis).toHaveValue('120');
  await expect(limits).toContainText('Now 2m 0s.');
  await analysis.fill('0');
  await expect(limits).toContainText('At least 1 second.');
  await expect(limits.getByRole('button', { name: 'Save time limits' })).toBeDisabled();
  await analysis.fill('900');
  await limits.getByRole('button', { name: 'Save time limits' }).click();
  await expect(page.getByText('Time limits saved.')).toBeVisible();
  expect(daemon.mutations('/setup/timeouts')[0].body).toMatchObject({ project_id: 'p1', timeouts: { analysis_seconds: 900, import_seconds: 300 } });
  expect(daemon.mutations('/setup')).toHaveLength(0);
  await expect(analysis).toHaveValue('900');
  await expect(limits.getByRole('button', { name: 'Save time limits' })).toBeDisabled();
});
