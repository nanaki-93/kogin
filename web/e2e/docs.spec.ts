// Docs: the guide to the project, its walkthroughs and component pages, the
// refresh, and asking the code.

import { expect, test } from './support/daemon';

test('the docs open on what the project is for, where it starts and how its features run', async ({ page }) => {
  await page.goto('/#/docs/overview');
  await expect(page.getByText('The overview is out of date')).toBeVisible();
  await expect(page.getByText(/A local-first coding assistant: a Go daemon/)).toBeVisible();
  await expect(page.getByText('Go 443 · TypeScript 64 · 610 files · 77,400 lines · 5 components')).toBeVisible();
  const starts = page.getByRole('region', { name: 'Where it starts' });
  await expect(starts.getByRole('button', { name: 'cmd/daemon/main.go' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Run it' })).toContainText('go test ./...');
  await page.getByRole('region', { name: 'How it works' }).getByRole('button', { name: 'Serve the Web UI' }).click();
  await expect(page).toHaveURL(/#\/docs\/flows\?n=1/);
  await expect(page.getByRole('heading', { name: 'Serve the Web UI', level: 1 })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Serve the Web UI' })).toHaveAttribute('aria-current', 'page');
  await page.getByRole('button', { name: "Start a task's agents" }).click();
  await expect(page).toHaveURL(/#\/docs\/flows\?n=2/);
  await page.getByRole('button', { name: 'internal/api/handlers/change_handler.go:88' }).click();
  await expect(page).toHaveURL(/#\/code\?file=internal%2Fapi%2Fhandlers%2Fchange_handler\.go&line=88/);
});

test('a component page says how it connects, which walkthroughs pass through it and what each file does', async ({ page }) => {
  await page.goto('/#/docs/architecture');
  const lines = page.locator('.doc ul.bulleted li');
  await expect(lines).toHaveCount(3);
  await expect(lines.first()).toContainText('cmd/daemon: wires the routes in main and starts the loopback server.');
  const diagram = page.getByRole('img', { name: '5 components and the imports between them' });
  await expect(page.getByText('Drawn from the code')).toBeVisible();
  await diagram.getByRole('button', { name: 'internal/app: open its page' }).click();
  await expect(page).toHaveURL(/#\/docs\/components\?path=internal%2Fapp/);
  await expect(page.getByRole('heading', { name: 'internal/app', level: 1 })).toBeVisible();
  await expect(page.getByText('Workflow orchestration.')).toBeVisible();
  const relations = page.locator('.relations');
  await expect(relations).toContainText('Used by');
  await expect(relations.getByRole('button', { name: "Start a task's agents" })).toBeVisible();
  await relations.getByRole('button', { name: 'internal/api' }).click();
  await expect(page.getByRole('heading', { name: 'internal/api', level: 1 })).toBeVisible();
  await expect(page.getByText('Request policy')).toBeVisible();
  const files = page.getByRole('region', { name: /^Files in internal\/api/ }).first();
  await expect(files).toContainText('Maps app errors to JSON responses');
  await page.getByRole('button', { name: 'webui.go' }).click();
  await expect(page).toHaveURL(/#\/code\?file=internal%2Fapi%2Fhandlers%2Fwebui\.go/);
});

test('where to change lists the usual changes, and names in docs prose open the code', async ({ page }) => {
  await page.goto('/#/docs/overview');
  await page.getByRole('region', { name: 'Where to change' }).getByRole('button', { name: 'Add an API route' }).click();
  await expect(page).toHaveURL(/#\/docs\/changes\?n=1/);
  await expect(page.getByRole('heading', { name: 'Add an API route', level: 1 })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Add an API route' })).toHaveAttribute('aria-current', 'page');
  await page.locator('.doc > p').getByRole('button', { name: 'main' }).click();
  await expect(page).toHaveURL(/#\/code\?file=cmd%2Fdaemon%2Fmain\.go&line=40/);
  await page.goto('/#/docs/architecture');
  await page.locator('.doc ul.bulleted').getByRole('button', { name: 'internal/project' }).click();
  await expect(page.getByRole('heading', { name: 'internal/project', level: 1 })).toBeVisible();
});

test('get started lists the declared commands by where they run, and dependencies by manifest', async ({ page }) => {
  await page.goto('/#/docs/runbook');
  await expect(page.getByRole('heading', { name: 'Get started', level: 1 })).toBeVisible();
  const web = page.getByRole('region', { name: 'Commands in web' });
  await expect(web).toContainText('npm run dev');
  await expect(web).toContainText('web/package.json');
  await expect(page.getByRole('region', { name: 'Commands in the project root' })).toContainText('go vet ./...');
  await page.getByRole('link', { name: /Dependencies/ }).click();
  const npm = page.getByRole('region', { name: 'Declared in web/package.json' });
  await expect(npm.getByRole('row').filter({ hasText: 'vite' })).toContainText('dev');
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

test('the docs ask box opens Ask the code with the question', async ({ page }) => {
  await page.goto('/#/docs/overview');
  await page.getByLabel('Ask the code a question').fill('where are tasks stored');
  await page.getByLabel('Ask the code a question').press('Enter');
  await expect(page).toHaveURL(/#\/docs\/ask\?q=where/);
  await expect(page.getByLabel('Question about the code')).toHaveValue('where are tasks stored');
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
