// Health: the finish line against the project profile, the found/fixed
// trend, the tests as they run and tasks that improve the code.

import { test, expect } from './support/daemon';

test('health shows the finish line and the trend, runs the tests and drafts a task', async ({ page, daemon }) => {
  await page.goto('/#/health');
  const finish = page.getByRole('region', { name: 'Finish line' });
  await expect(finish).toContainText('5 issues to the finish line');
  await expect(finish).toContainText('Counting medium or above · tests not run on this version · default profile');
  await expect(finish).toContainText('123 in a test file');
  const trend = page.getByRole('region', { name: 'Found and fixed' });
  await expect(trend).toContainText('Not converging');
  await expect(trend).toContainText('Last 7 days: 310 found, 109 fixed.');
  await expect(trend.getByRole('img', { name: /Issues found and fixed per day/ })).toBeVisible();
  const tests = page.getByRole('region', { name: 'Tests' });
  await expect(tests).toContainText('Not run yet');
  await expect(tests).toContainText('cmd/');

  await page.getByRole('button', { name: 'Run tests' }).click();
  await expect(tests).toContainText('85%', { timeout: 10_000 });
  await expect(tests.getByRole('table', { name: 'Coverage by package' })).toContainText('internal/utils');
  expect(daemon.mutations('/health/tests')).toHaveLength(1);

  await finish.getByRole('button', { name: '123 in a test file' }).click();
  await expect(page).toHaveURL(/#\/issues\?scope=out/);
  await page.goBack();

  const tasks = page.getByRole('region', { name: 'Improve the health' });
  await tasks.getByRole('listitem').filter({ hasText: 'Gather 8 of the 12 test files' }).getByRole('button', { name: 'Draft task' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByPlaceholder('What should change?')).toHaveValue('Gather 8 of the 12 test files of master.go into master_test.go');
  await expect(dialog).toContainText('master_kdf_test.go');
});

test('the project profile is saved and a kind brings its security level', async ({ page, daemon }) => {
  await page.goto('/#/settings/profile');
  const form = page.getByRole('region', { name: 'Project profile' });
  await expect(form).toContainText('Not set yet');
  await form.getByRole('radio', { name: 'Personal' }).click();
  await expect(form.getByRole('radio', { name: 'Basic' })).toHaveAttribute('aria-checked', 'true');
  await form.getByLabel('Purpose').fill('A command-line password manager for my own use.');
  await form.getByLabel('Shape').selectOption('cli');
  await form.getByRole('checkbox', { name: 'macOS' }).check();
  await form.getByRole('button', { name: 'Save profile' }).click();
  await expect(page.getByText('Project profile saved.')).toBeVisible();
  const saved = daemon.mutations('/profile');
  expect(saved).toHaveLength(1);
  expect(saved[0].body).toMatchObject({ project_revision: 'r42', profile: { kind: 'personal', security: 'basic', shape: 'cli', platforms: ['macos'], finish_line: 'medium', analyze_tests: false } });
  expect((saved[0].body!.profile as Record<string, unknown>).saved).toBeUndefined();
  await expect(form).not.toContainText('Not set yet');
});
