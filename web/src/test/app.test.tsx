// Renders the whole app against a fake daemon to check the main workflows
// wire up: inbox triage, the review gate and the open-project empty state.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { App, createQueryClient } from '@/App';

const project = { project_id: 'p1', project_revision: 'r1', name: 'demo', path: '/src/demo', type: 'go', file_count: 3 };
const file = (path: string) => ({ path, language: 'go', line_count: 10 });
const session = {
  ...project, id: 'chg-1', kind: 'fix', title: 'Reject nested asset paths', acceptance_criteria: ['Nested paths return 404'],
  revision: 2, hash: 'sha-2', state: 'checked', freshness: 'current', updated_at: '2026-10-01T00:00:00Z', worktree: { base: 'abc', branch: 'orca/reject-nested-a1b2c3' },
  targets: [{ path: 'web.go' }, { path: 'web_test.go' }], messages: [],
  changes: [
    { path: 'web.go', content: '', hash: 'a', diff: { lines: [{ kind: 'removed', old_line: 1, text: 'old' }, { kind: 'added', new_line: 1, text: 'new' }] } },
    { path: 'web_test.go', content: '', hash: 'b', diff: { lines: [{ kind: 'added', new_line: 1, text: 'func TestX(t *testing.T) {}' }] } },
  ],
  reviewed_files: ['web_test.go'], file_review_hash: 'sha-2',
  checks: [{ name: 'tests', required: true, state: 'passed' }],
  workflow: { id: 'wf', status: 'awaiting_human_review', stages: [{ name: 'create', status: 'completed' }, { name: 'human_review', status: 'waiting' }], specialists: [] },
};

type Route = (path: string, init?: RequestInit) => unknown;

function fakeDaemon(overrides: Record<string, Route> = {}) {
  const routes: Record<string, Route> = {
    '/status': () => ({ version: '9.9.9' }),
    '/api/models/current': () => ({ scopes: {} }),
    '/api/projects/current': () => project,
    '/api/projects/current/index': () => ({ files: [file('web.go'), file('web_test.go'), file('main.go')] }),
    '/api/projects/current/execution-trust': () => ({ trusted: true }),
    '/api/projects/current/git/workspace': () => ({ available: true, branch: 'main', head: 'abc', worktrees: [] }),
    '/api/projects/current/changes': () => [{ ...session, workflow_status: 'awaiting_human_review' }],
    '/api/projects/current/changes/chg-1': () => session,
    '/api/projects/current/findings': () => ({ findings: [] }),
    '/api/projects/current/overview': () => ({ analysis_coverage: { fresh: 1, stale: 0, missing: 0 } }),
    ...overrides,
  };
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split('?')[0];
    const route = routes[path];
    if (!route) return new Response(JSON.stringify({ message: 'not found' }), { status: 404 });
    try {
      return new Response(JSON.stringify(route(path, init)), { status: 200 });
    } catch (err) {
      return new Response(JSON.stringify({ user_message: (err as Error).message }), { status: 400 });
    }
  });
}

function renderApp(hash = '#/') {
  window.location.hash = hash;
  return render(<App client={createQueryClient()} />);
}

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = '';
});

describe('App', () => {
  it('lists tasks that need review in the inbox and opens one', async () => {
    vi.stubGlobal('fetch', fakeDaemon());
    renderApp();
    const row = await screen.findByText('Reject nested asset paths');
    expect(screen.getByText(/needs your review/i)).toBeInTheDocument();
    await userEvent.click(row);
    await waitFor(() => expect(window.location.hash).toBe('#/tasks/chg-1'));
    const gate = await screen.findByRole('complementary', { name: 'Review gate' });
    expect(within(gate).getByText('Files viewed')).toBeInTheDocument();
    expect(within(gate).getByText('1 of 2')).toBeInTheDocument();
    expect(within(gate).getByRole('button', { name: 'Accept & commit' })).toBeDisabled();
  });

  it('offers to open a project when none is active, without refetching in a loop', async () => {
    const fetch = fakeDaemon({
      '/api/projects/current': () => {
        throw new Error('active project has not been analyzed');
      },
    });
    vi.stubGlobal('fetch', fetch);
    renderApp();
    expect(await screen.findByText('Open a project to start')).toBeInTheDocument();
    await act(() => new Promise(resolve => setTimeout(resolve, 300)));
    const projectCalls = fetch.mock.calls.filter(([url]) => String(url) === '/api/projects/current');
    expect(projectCalls.length).toBeLessThanOrEqual(2);
  });

  it('opens the command palette with the keyboard and jumps to a file', async () => {
    vi.stubGlobal('fetch', fakeDaemon({ '/api/projects/current/files/info': () => ({ path: 'main.go', language: 'go', content: 'package main\n' }) }));
    renderApp();
    await screen.findByText('Reject nested asset paths');
    await userEvent.keyboard('{Control>}k{/Control}');
    const input = await screen.findByPlaceholderText(/search files, tasks/i);
    await userEvent.type(input, 'main.go');
    await userEvent.keyboard('{Enter}');
    await waitFor(() => expect(window.location.hash).toContain('#/code?file=main.go'));
  });
});
