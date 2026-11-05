// The review and workspace experiments: agents at work, the project map,
// tasks from a sentence in the palette, and the Inbox's next step.

import { expect, test } from './support/daemon';
import * as data from './support/fixtures';

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString();

test('a running task shows the agent at work and can be stopped from there', async ({ page, daemon }) => {
  const task = data.reviewTask();
  daemon.sessions.set('chg-1', { ...task, workflow: { ...task.workflow!, id: 'wf-9', status: 'running', stages: [{ name: 'create', status: 'running', started_at: ago(40), model: task.workflow!.stages[0].model }, { name: 'test', status: 'pending' }, { name: 'review', status: 'pending' }, { name: 'human_review', status: 'blocked' }] } });
  daemon.serve('/api/projects/current/changes/chg-1/activity', () => ({
    workflow_id: 'wf-9',
    entries: [
      { seq: 1, at: ago(38), role: 'coder', tool: 'read_file', target: 'internal/api/handlers/webui.go', status: 'done' },
      { seq: 2, at: ago(30), role: 'coder', tool: 'search', target: 'ReadFile(', status: 'done' },
      { seq: 3, at: ago(20), role: 'coder', tool: 'edit_file', target: 'internal/api/handlers/webui.go', status: 'failed', detail: 'old text not found' },
      { seq: 4, at: ago(12), role: 'coder', tool: 'edit_file', target: 'internal/api/handlers/webui.go', status: 'done' },
      { seq: 5, at: ago(2), role: 'coder', tool: 'run_checks', status: 'running' },
    ],
  }));
  await page.goto('/#/tasks/chg-1');
  const live = page.getByRole('region', { name: 'Agents at work' });
  await expect(live).toContainText('Creation agent is running the checks');
  const calls = live.getByRole('list', { name: 'Tool calls' });
  await expect(calls.getByRole('listitem')).toHaveCount(5);
  await expect(calls).toContainText('Searched');
  await expect(calls).toContainText('old text not found');
  await expect(live).toContainText('Changed so far · 1 file');
  await expect(live).toContainText('5 tool calls');

  await live.getByRole('button', { name: 'Stop' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Stop agents' }).click();
  await expect(page.getByText('Agents stopped.')).toBeVisible();
  expect(daemon.mutations('/workflow/cancel')[0].body).toMatchObject({ workflow_id: 'wf-9' });
  await expect(live).toBeHidden();
});

test('the project map sizes folders by lines, colors them by open issues and zooms into a folder', async ({ page }) => {
  await page.goto('/#/docs/map');
  await expect(page.getByRole('heading', { name: 'Project map' })).toBeVisible();
  const frame = page.locator('.map-frame');
  // The issue's folder carries its worst severity.
  const api = frame.getByRole('button', { name: /^internal\/api\/handlers: 2 files/ });
  await expect(api).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Map trail' })).toContainText('kogin');
  await api.click();
  await expect(page).toHaveURL(/path=internal%2Fapi%2Fhandlers/);
  await expect(page.getByRole('navigation', { name: 'Map trail' })).toContainText('api/handlers');
  // Backspace goes back up, one folder at a time.
  await page.keyboard.press('Backspace');
  await expect(page).toHaveURL(/path=internal$/);
  await page.keyboard.press('Backspace');
  await expect(page).not.toHaveURL(/path=/);
  await expect(frame.getByRole('button', { name: /^internal\/llm\/client\.go: .*1 open issue/ })).toBeVisible();
  await page.getByRole('radio', { name: 'Language' }).click();
  await expect(page.locator('.map-legend')).toContainText('go');
});

test('a sentence in the palette drafts the task for the open issue it describes', async ({ page, daemon }) => {
  await page.goto('/#/');
  await page.keyboard.press('Control+k');
  await page.getByRole('combobox').fill('> retry loop in the llm client ignores cancellation, fix it');
  const palette = page.getByRole('dialog', { name: 'Command palette' });
  await expect(palette).toContainText('Retry loop ignores cancellation');
  await expect(palette).toContainText('Fixes the open issue');
  await expect(palette).toContainText('internal/llm/client.go');
  await palette.getByRole('option', { name: /Edit it first/ }).click();
  // The composer opens with the daemon's draft for that issue.
  await expect(page.getByRole('dialog', { name: 'New task' })).toBeVisible();
  expect(daemon.requests.some(request => request.path === '/api/projects/current/findings/f2/task')).toBe(true);
});

test('the Inbox puts the next step on top; Skip shows the one after', async ({ page }) => {
  await page.goto('/#/');
  const next = page.getByRole('region', { name: 'Next up' });
  await expect(next).toContainText('Review “Reject nested asset paths in the Web UI handler”');
  await next.getByRole('button', { name: 'Skip' }).click();
  await expect(next).toContainText('See why “Cache the parsed index” stopped');
  await page.keyboard.press('s');
  await expect(next).toContainText('Merge mo/chg-7');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/git\?path=/);
});
