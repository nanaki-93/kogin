import { describe, expect, it } from 'vitest';
import { fuzzyScore, rankItems } from './fuzzy';
import { ancestors, buildTree, visibleRows } from './tree';
import { buildHash, parseHash } from './router';
import { escapeHtml, highlightLines } from './highlight';
import { companionTestPath, explainable, formatElapsed, isTestPath, newFilePath, timeAgo } from './util';
import { changeOverlaps, composerStartBlocker, ownWorktreeBlocker, parseCommits, pauseTitle, runStartOptions, stageToolsLabel, taskFixingIssue, workflowBlocker, workflowDestinations, workflowRequest, workflowStages, worktreeRemovable, worktreeTasks } from './workflow';
import type { ChangeHistoryEntry, Finding, GitWorkspace, GitWorktree } from '@/api/types';
import { isInProgressFinding, isOpenFinding, isUnresolvedFinding } from '@/components/domain';
import { limitErrors, modelLocation, roleModelOptions, selectedModel } from './agentSettings';
import { applyEvent, finishedRun, parseFrames } from '@/api/events';
import { QueryClient } from '@tanstack/react-query';
import { costCaveat, costLabel, formatTokens, usageLabel } from './usage';
import { ApiError, createApi, documentWriteToken, errorMessage, query, withWorktree } from '@/api/client';

describe('explainable', () => {
  it('follows the daemon: source files in any language, not prose or binaries', () => {
    expect(explainable({ language: 'Go' })).toBe(true);
    expect(explainable({ language: 'TypeScript' })).toBe(true);
    expect(explainable({ language: 'Python' })).toBe(true);
    expect(explainable({ language: 'Markdown' })).toBe(false);
    expect(explainable({ language: 'Text' })).toBe(false);
    expect(explainable({ language: 'Go', binary: true })).toBe(false);
  });
});

describe('fuzzy', () => {
  it('prefers basename and contiguous hits', () => {
    const files = ['internal/api/handlers/webui_handler.go', 'internal/app/handler.go', 'docs/handbook.md'];
    expect(rankItems(files, 'handler', f => f)[0].item).toBe('internal/app/handler.go');
    expect(rankItems(files, 'wuh', f => f).map(r => r.item)).toEqual(['internal/api/handlers/webui_handler.go']);
    expect(fuzzyScore('zz', 'abc')).toBeNull();
    expect(fuzzyScore('abc', 'xxabc')?.positions).toEqual([2, 3, 4]);
    expect(rankItems(files, '', f => f, 2)).toHaveLength(2);
  });
});

describe('tree', () => {
  it('compacts single-child folders and lists folders before files', () => {
    const tree = buildTree(['internal/api/a.go', 'internal/api/b.go', 'main.go'], p => p);
    expect(visibleRows(tree, new Set()).map(r => [r.type, r.label])).toEqual([['dir', 'internal/api'], ['file', 'main.go']]);
    expect(visibleRows(tree, new Set(['internal/api'])).map(r => r.label)).toEqual(['internal/api', 'a.go', 'b.go', 'main.go']);
    expect(ancestors('a/b/c.go')).toEqual(['a', 'a/b']);
  });
});

describe('router', () => {
  it('round-trips paths and params', () => {
    const hash = buildHash('/tasks/chg 1/files', { file: 'a/b.go', wt: '/tmp/wt', empty: '' });
    expect(hash).toBe('#/tasks/chg%201/files?file=a%2Fb.go&wt=%2Ftmp%2Fwt');
    const route = parseHash(hash);
    expect(route.segments).toEqual(['tasks', 'chg 1', 'files']);
    expect(route.params.get('wt')).toBe('/tmp/wt');
    expect(parseHash('').path).toBe('/');
  });
});

describe('highlight', () => {
  it('escapes markup and keeps one entry per line with balanced spans', () => {
    expect(escapeHtml('<a href="x">&</a>')).toBe('&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;');
    const out = highlightLines('/* a\nb */\nconst x = "<y>";', 'javascript');
    expect(out).toHaveLength(3);
    for (const line of out) expect((line.match(/<span/g) ?? []).length).toBe((line.match(/<\/span>/g) ?? []).length);
    expect(out[2]).not.toContain('<y>');
  });
});

describe('util and workflow', () => {
  it('recognizes tests and companions', () => {
    expect(isTestPath('a/b_test.go')).toBe(true);
    expect(isTestPath('web/x.test.tsx')).toBe(true);
    expect(isTestPath('a/b.go')).toBe(false);
    expect(companionTestPath('a/b.go')).toBe('a/b_test.go');
    expect(companionTestPath('a/b_test.go')).toBe('');
    expect(timeAgo('2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:05:00Z'))).toMatch(/5m/);
    expect(formatElapsed(0)).toBe('0:00');
    expect(formatElapsed(7_900)).toBe('0:07');
    expect(formatElapsed(125_000)).toBe('2:05');
    expect(formatElapsed(-5)).toBe('0:00');
  });

  it('accepts only a relative file path as a new file', () => {
    expect(newFilePath(' cmd/util_test.go ')).toBe('cmd/util_test.go');
    expect(newFilePath('./cmd/util_test.go')).toBe('cmd/util_test.go');
    expect(newFilePath('README.md')).toBe('README.md');
    for (const rejected of ['', 'util', 'cmd/', '/etc/passwd.go', '../x.go', 'cmd/../x.go', 'cmd//x.go', 'cmd/./x.go', 'a\\b.go', 'cmd/.env', 'cmd/x.', 'cmd/x\n.go', 'a/' + 'b'.repeat(512) + '.go']) {
      expect(newFilePath(rejected), rejected).toBe('');
    }
  });

  it('parses commits and explains workflow blockers', () => {
    expect(parseCommits('abc123 Fix thing\n\ndef456')).toEqual([{ sha: 'abc123', subject: 'Fix thing' }, { sha: 'def456', subject: '' }]);
    const s = { state: 'draft', freshness: 'current', kind: 'fix', targets: [{ path: 'a.go' }] };
    expect(workflowBlocker(s, true, isTestPath)).toMatch(/test file/);
    expect(workflowBlocker({ ...s, targets: [{ path: 'a_test.go' }] }, false, isTestPath)).toMatch(/Trust/);
    expect(workflowBlocker({ ...s, targets: [{ path: 'a_test.go' }] }, true, isTestPath)).toBe('');
    // In its own worktree, the testing agent creates the test files it needs.
    expect(workflowBlocker({ ...s, worktree: { base: 'b', branch: 'mo/x' } }, true, isTestPath)).toBe('');
    expect(workflowBlocker({ ...s, workflow: { id: 'w', status: 'paused', stages: [] } as never, targets: [{ path: 'a_test.go' }] }, true, isTestPath)).toBe('');
    expect(workflowBlocker(null, true, isTestPath)).toMatch(/Select/);
  });

  it('says why the composer cannot start agents', () => {
    expect(composerStartBlocker([], true, true, isTestPath)).toMatch(/at least one file/);
    expect(composerStartBlocker(['cmd/master.go'], false, true, isTestPath)).toMatch(/test file/);
    // In its own worktree, the testing agent creates the test files it needs.
    expect(composerStartBlocker(['cmd/master.go'], true, true, isTestPath)).toBe('');
    expect(composerStartBlocker(['cmd/master.go', 'cmd/master_test.go'], false, true, isTestPath)).toBe('');
    expect(composerStartBlocker(['cmd/master.go'], true, false, isTestPath)).toMatch(/Trust/);
  });
});

describe('issues being fixed', () => {
  const finding = (status: string): Finding => ({ id: 'f1', source: 'ai', severity: 'high', title: 't', message: 'm', location: { path: 'a.go' }, status });

  it('takes an issue a task is fixing out of the open issues, but not out of the code', () => {
    expect([isOpenFinding(finding('open')), isInProgressFinding(finding('open')), isUnresolvedFinding(finding('open'))]).toEqual([true, false, true]);
    expect([isOpenFinding(finding('in_progress')), isInProgressFinding(finding('in_progress')), isUnresolvedFinding(finding('in_progress'))]).toEqual([false, true, true]);
    for (const status of ['dismissed', 'fixed', 'retired']) expect([isOpenFinding(finding(status)), isUnresolvedFinding(finding(status))]).toEqual([false, false]);
  });

  it('finds the newest live task fixing an issue', () => {
    const target = { project_id: 'p', kind: 'semantic', id: 'f1', path: 'a.go' };
    const entry = (id: string, state: string, worktreePath: string, targets = [target]) => ({ id, state, worktreePath, finding_targets: targets });
    const tasks = [entry('other', 'draft', '', [{ ...target, id: 'f2' }]), entry('applied-main', 'applied', ''), entry('newest', 'draft', '/p/.worktrees/b'), entry('older', 'draft', '')];
    expect(taskFixingIssue(tasks, 'f1')?.id).toBe('newest');
    // Applied in its worktree, the fix is not merged yet; applied in the main checkout, it is done.
    expect(taskFixingIssue([entry('committed', 'applied', '/p/.worktrees/c')], 'f1')?.id).toBe('committed');
    expect(taskFixingIssue([entry('applied-main', 'applied', '')], 'f1')).toBeUndefined();
    expect(taskFixingIssue(tasks, 'f3')).toBeUndefined();
  });
});

describe('task worktrees', () => {
  const task = (id: string, extra: Partial<ChangeHistoryEntry> = {}): ChangeHistoryEntry => ({ id, project_id: 'p', project_revision: 'r', kind: 'fix', title: id, revision: 1, hash: 'h', state: 'draft', freshness: 'current', updated_at: '', ...extra });
  const tree = (extra: Partial<GitWorktree> = {}): GitWorktree => ({ path: '/p/.worktrees/a', head: 'aa', branch: 'orca/a', integration: 'merged', dirty: false, sessions: [], ...extra });
  const done = task('done', { state: 'applied', commit: { state: 'committed', oid: 'c1' } });

  it('lists each task of a worktree once', () => {
    expect(worktreeTasks(tree({ sessions: [task('a'), done], completed: [done] })).map(t => t.id)).toEqual(['a', 'done']);
  });

  it('removes only a clean worktree whose work the main branch has', () => {
    expect(worktreeRemovable(tree())).toBe(true);
    expect(worktreeRemovable(tree({ sessions: [done], completed: [done] }))).toBe(true);
    expect(worktreeRemovable(tree({ sessions: [task('draft')] }))).toBe(false);
    for (const extra of [{ integration: 'up_to_date' }, { integration: 'behind' }, { dirty: true }, { locked: true }, { error: 'x' }, { branch: '' }, { current: true }]) {
      expect(worktreeRemovable(tree(extra)), JSON.stringify(extra)).toBe(false);
    }
  });

  it('says why a task cannot get its own worktree', () => {
    const git = { available: true, branch: 'main', head: 'aa' } as GitWorkspace;
    expect(ownWorktreeBlocker(git, true)).toBe('');
    expect(ownWorktreeBlocker(git, false)).toMatch(/trust/);
    expect(ownWorktreeBlocker({ ...git, head: '' }, true)).toMatch(/commit/);
    expect(ownWorktreeBlocker(undefined, true)).toMatch(/Git repository/);
  });
});

describe('running the agents again', () => {
  const stages = (create: string) => [
    { name: 'create', status: 'completed', model: { scope: 'bug', profile: create, model: 'creator' } },
    { name: 'test', status: 'completed', model: { scope: 'code', profile: 'code', model: 'tester' } },
    { name: 'review', status: 'failed', model: { scope: 'analyze', profile: 'analyze', model: 'critic' } },
    { name: 'human_review', status: 'blocked' },
  ];
  const revised = { changes: [{ path: 'a.go' }, { path: 'a_test.go' }], workflow: { id: 'wf', status: 'outdated', stages: stages('bug') } };

  it('keeps a revised proposal by default and writes it from the brief when there is none', () => {
    expect(runStartOptions({ changes: [] }, 'bug', isTestPath)).toEqual({ options: [], initial: 'create' });
    const starts = runStartOptions(revised, 'bug', isTestPath);
    expect(starts.initial).toBe('checks');
    expect(starts.options.map(option => [option.value, option.blocked])).toEqual([['checks', ''], ['test', ''], ['create', '']]);
    // A proposal without tests starts by writing them.
    expect(runStartOptions({ ...revised, changes: [{ path: 'a.go' }] }, 'bug', isTestPath).initial).toBe('test');
    // A manual proposal has no earlier run whose models must match.
    expect(runStartOptions({ changes: revised.changes }, 'code', isTestPath).initial).toBe('checks');
  });

  it('writes the change again when the kept creation model changed', () => {
    const starts = runStartOptions(revised, 'code', isTestPath);
    expect(starts.initial).toBe('create');
    expect(starts.options.filter(option => option.blocked).map(option => option.value)).toEqual(['checks', 'test']);
    expect(starts.options[0].blocked).toMatch(/creation model changed/);
  });

  it('continues a paused run where it stopped', () => {
    const paused = (resume: string) => ({ ...revised, workflow: { ...revised.workflow, status: 'paused', pause: { stage: resume === 'checks' ? 'review' : resume, resume_from: resume, provider: 'claude' } } });
    const review = runStartOptions(paused('review'), 'bug', isTestPath);
    expect(review.initial).toBe('review');
    expect(review.options.map(option => option.value)).toEqual(['review', 'checks', 'test', 'create']);
    expect(runStartOptions(paused('test'), 'bug', isTestPath).initial).toBe('test');
    expect(runStartOptions(paused('create'), 'bug', isTestPath).initial).toBe('create');
    // A changed creation model still writes the change again.
    expect(runStartOptions(paused('review'), 'code', isTestPath).initial).toBe('create');
    expect(workflowRequest({ project_id: 'p', project_revision: 'r', revision: 1, hash: 'h' }, 'Fix', { selections: { create: 'bug', test: 'code', review: 'analyze' }, destinations: [], confirmed: [], local: [] }, { start: 'review' }).resume_from).toBe('review');
  });

  it('says when a paused run can continue', () => {
    const now = new Date(2026, 9, 31, 10, 0);
    expect(pauseTitle({ stage: 'create', resume_from: 'create', provider: 'claude' }, now)).toBe('Paused: the Claude plan reached its usage limit');
    expect(pauseTitle({ stage: 'test', resume_from: 'test', provider: 'agy', resumes_at: new Date(2026, 9, 31, 15, 30).toISOString() }, now)).toMatch(/^Paused until the Google AI plan's limit resets at /);
    expect(pauseTitle({ stage: 'test', resume_from: 'test', provider: 'pi', resumes_at: new Date(2026, 10, 2, 9, 0).toISOString() }, now)).toMatch(/resets on .+ at /);
    expect(pauseTitle({ stage: 'test', resume_from: 'test', provider: 'claude', resumes_at: new Date(2026, 9, 31, 9, 0).toISOString() }, now)).toMatch(/has reset, resume the run/);
  });

  it('sends where the run starts, leaving the default unset', () => {
    const choice = { selections: { create: 'bug', test: 'code', review: 'analyze' }, destinations: [], confirmed: [], local: [] };
    const identity = { project_id: 'p', project_revision: 'r', revision: 4, hash: 'h' };
    expect(workflowRequest(identity, 'Fix it', choice, { start: 'checks' }).resume_from).toBe('checks');
    expect(workflowRequest(identity, 'Fix it', choice, { start: 'create' }).resume_from).toBeUndefined();
    expect(workflowRequest(identity, 'Fix it', choice).resume_from).toBeUndefined();
  });

  it('names a checks start\'s test stage for what it does until a repair', () => {
    const run = { id: 'wf', status: 'running', resume_from: 'checks', stages: stages('bug') };
    expect(workflowStages(run)[1]).toMatchObject({ label: 'Run the checks', actor: 'Checks', model: '' });
    expect(workflowStages({ ...run, auto_repair_attempts: 1 })[1]).toMatchObject({ label: 'Write and run tests', model: 'tester' });
    expect(workflowStages({ ...run, resume_from: undefined })[1].label).toBe('Write and run tests');
  });

  it('says how a stage\'s agent worked with tools', () => {
    expect(stageToolsLabel(undefined)).toBe('');
    expect(stageToolsLabel({ calls: 34, turns: 12, finished: true, summary: 'read_file 20, edit_file 14' })).toBe('Worked with tools: 34 calls in 12 turns (read_file 20, edit_file 14)');
    expect(stageToolsLabel({ calls: 1, turns: 1, finished: false })).toBe('Worked with tools: 1 call in 1 turn; stopped before finishing');
  });
});

describe('events', () => {
  it('parses complete frames and returns the tail', () => {
    const seen: [string, string][] = [];
    const tail = parseFrames('event: update\ndata: {"kind":"workflow"}\n\n: ping\n\nevent: update\ndata: {"ki', (e, d) => seen.push([e, d]));
    expect(seen).toEqual([['update', '{"kind":"workflow"}']]);
    expect(tail).toBe('event: update\ndata: {"ki');
  });
});

describe('client', () => {
  it('builds queries and worktree params', () => {
    expect(query({ a: 1, b: '', c: undefined, d: true })).toBe('?a=1&d=true');
    expect(withWorktree('/x?a=1', '/w t')).toBe('/x?a=1&worktree_path=%2Fw%20t');
    expect(withWorktree('/x', '')).toBe('/x');
  });

  it('sends the Web UI header and surfaces daemon messages', async () => {
    const calls: RequestInit[] = [];
    const api = createApi(async (_url, init) => {
      calls.push(init!);
      return new Response(JSON.stringify({ user_message: 'Review every file first.' }), { status: 409 });
    });
    await expect(api.post('/x', { a: 1 })).rejects.toMatchObject({ message: 'Review every file first.', status: 409 });
    expect((calls[0].headers as Record<string, string>)['X-Mini-Orca-Web-UI']).toBe('1');
    expect(calls[0].body).toBe('{"a":1}');
  });

  it('adds the daemon detail to a failure message once', () => {
    expect(errorMessage(new ApiError('Review the request and try again.', 400, { detail: 'saved analysis progress is unreadable' }))).toBe('Review the request and try again. (saved analysis progress is unreadable)');
    expect(errorMessage(new ApiError('Disk full', 500, { detail: 'Disk full' }))).toBe('Disk full');
    expect(errorMessage(new Error('plain'))).toBe('plain');
  });
});

describe('changeOverlaps', () => {
  it('flags shared files, packages and build manifests above a task', () => {
    expect(changeOverlaps(['internal/app/a.go'], ['internal/app/b.go'])).toBe(true);
    expect(changeOverlaps(['internal/app/a.go'], ['internal/llm/c.go'])).toBe(false);
    expect(changeOverlaps(['go.mod'], ['internal/llm/c.go'])).toBe(true);
    expect(changeOverlaps(['web/package.json'], ['web/src/app.tsx'])).toBe(true);
    expect(changeOverlaps(['web/package.json'], ['internal/app/a.go'])).toBe(false);
    expect(changeOverlaps(['README.md'], ['internal/app/a.go'])).toBe(false);
  });
});

describe('usage', () => {
  it('formats tokens, calls and estimated or partial costs', () => {
    expect([formatTokens(950), formatTokens(1250), formatTokens(20_500), formatTokens(1_250_000)]).toEqual(['950', '1.3k', '21k', '1.3M']);
    expect(usageLabel({ model_calls: 1, input_tokens: 900, output_tokens: 100 })).toBe('1 call · 1.0k tokens');
    expect(usageLabel({ model_calls: 0, input_tokens: 0, output_tokens: 0 })).toBe('');
    expect(costLabel({ status: 'estimated', currency: 'USD', known_subtotal: 0.5, total: 0.5, reasons: [] })).toMatch(/0\.5/);
    expect(costLabel({ status: 'unknown', currency: 'USD', known_subtotal: 0.25, reasons: ['model_rate_unavailable'] })).toMatch(/^≥ /);
    expect(costLabel({ status: 'unknown', known_subtotal: 0, reasons: ['price_table_unavailable'] })).toBe('');
    expect(costCaveat({ status: 'unknown', known_subtotal: 0, reasons: ['price_table_unavailable'] })).toMatch(/telemetry\.prices/);
  });
});

describe('daemon events', () => {
  it('announces finished runs only', () => {
    expect(finishedRun({ kind: 'workflow', stage: 'test', status: 'running', title: 'T' })).toBeNull();
    expect(finishedRun({ kind: 'workflow', status: 'canceled', title: 'T' })).toBeNull();
    expect(finishedRun({ kind: 'workflow', status: 'awaiting_human_review', title: 'T', usage: { model_calls: 2, input_tokens: 1000, output_tokens: 500 } })).toEqual({ message: 'Agents finished “T”. It needs your review.', detail: '2 calls · 1.5k tokens', tone: 'success' });
  });

  it('reloads only the task a workflow event names', () => {
    const client = new QueryClient();
    const invalidated: unknown[] = [];
    client.invalidateQueries = (filters?: { queryKey?: unknown }) => {
      invalidated.push(filters?.queryKey);
      return Promise.resolve();
    };
    applyEvent(client, { kind: 'workflow', session_id: 'chg-1' });
    expect(invalidated).toContainEqual(['session', 'chg-1']);
    expect(invalidated).not.toContainEqual(['session']);
    expect(invalidated).toContainEqual(['usage']);
  });
});

describe('write token', () => {
  it('goes with every request that changes state, never with reads', async () => {
    const seen: Record<string, Record<string, string>> = {};
    const api = createApi(async (url, init) => {
      seen[(init?.method ?? 'GET') + ' ' + String(url)] = init?.headers as Record<string, string>;
      return new Response('{}', { status: 200 });
    }, () => 'launch-token');
    await api.get('/api/projects/current');
    await api.post('/api/projects/current/changes', {});
    expect(seen['GET /api/projects/current']['X-Mini-Orca-Token']).toBeUndefined();
    expect(seen['POST /api/projects/current/changes']['X-Mini-Orca-Token']).toBe('launch-token');
  });

  it('reads the token the daemon wrote into the document', () => {
    const doc = document.implementation.createHTMLDocument('ui');
    doc.head.innerHTML = '<meta name="mini-orca-write-token" content="__MINI_ORCA_WRITE_TOKEN__">';
    expect(documentWriteToken(doc)).toBe('');
    doc.head.innerHTML = '<meta name="mini-orca-write-token" content="abc123">';
    expect(documentWriteToken(doc)).toBe('abc123');
  });
});

describe('agent settings', () => {
  const scopes = {
    analyze: { scope: 'analyze', model: 'gemini-2.5-pro', remote_provider: true, provider_origin: 'googleapis.com' },
    bug: { scope: 'bug', model: 'qwen3-coder', remote_provider: false },
    code: { scope: 'code', model: 'qwen3-coder', remote_provider: false },
  };
  const agy = { scope: '', model: 'gemini-3.8-pro', remote_provider: true, provider_origin: 'cli://agy' };
  const catalog = {
    models: [
      { id: 'configured:aa', name: 'gemini-2.5-pro', provider: 'gemini', source: 'configured', location: 'remote', model: scopes.analyze },
      { id: 'agy:gemini-3.8-pro', name: 'Gemini 3.8 Pro', provider: 'agy', source: 'agy', location: 'unknown', model: agy },
      { id: 'claude:sonnet', name: 'Claude Sonnet', provider: 'anthropic', source: 'claude', location: 'remote', model: { scope: '', model: 'sonnet', remote_provider: true, provider_origin: 'cli://claude' } },
    ],
    defaults: {}, pi: { status: 'unavailable' }, agy: { status: 'ready' }, claude: { status: 'ready' },
  };

  it('offers the scopes, then listed CLI models, and keeps an unlisted selection', () => {
    const groups = roleModelOptions(scopes, catalog, ['bug', 'pi:local/llama'], { 'pi:local/llama': { scope: 'bug', model: 'llama' } });
    expect(groups.map(g => g.label)).toEqual(['Configured in config.yaml', 'Claude Code models', 'Agy models', 'Current selection']);
    expect(groups[0].options.map(o => o.label)).toEqual(['analyze · gemini-2.5-pro', 'bug · qwen3-coder', 'code · qwen3-coder']);
    expect(groups[1].options[0]).toMatchObject({ id: 'claude:sonnet', label: 'Claude Sonnet (claude:sonnet)' });
    expect(groups[2].options[0]).toMatchObject({ id: 'agy:gemini-3.8-pro', label: 'Gemini 3.8 Pro (agy:gemini-3.8-pro)' });
    expect(groups[3].options[0].label).toBe('llama (pi:local/llama)');
    expect(modelLocation(selectedModel(groups, 'agy:gemini-3.8-pro'))).toEqual({ remote: true, label: 'remote · cli://agy' });
    expect(modelLocation(selectedModel(groups, 'bug')).label).toBe('local');
  });

  it('bounds the budget by the configured ceilings and lets repairs be zero', () => {
    const ceilings = { model_calls: 40, tool_calls: 120, input_tokens: 1000, output_tokens: 100, elapsed_seconds: 60, concurrent_calls: 2, repairs: 2 };
    expect(limitErrors({ ...ceilings, repairs: 0 }, ceilings)).toEqual({});
    expect(limitErrors({ ...ceilings, model_calls: 41, tool_calls: 0, output_tokens: 1.5 }, ceilings)).toEqual({ model_calls: 'At most 40 (config.yaml).', tool_calls: 'At least 1.', output_tokens: 'At least 1.' });
  });

  it('confirms a role that runs on a remote catalog model', () => {
    const settings = { project_id: 'p', project_revision: 'r', state: 'overridden', roles: { creation: 'bug', testing: 'code', review: 'agy:gemini-3.8-pro' }, role_models: { review: agy } };
    const choice = workflowDestinations(settings, { scopes });
    expect(choice.selections).toEqual({ create: 'bug', test: 'code', review: 'agy:gemini-3.8-pro' });
    expect(choice.confirmed).toEqual(['agy:gemini-3.8-pro']);
    expect(choice.destinations).toEqual([{ stage: 'review', selection: 'agy:gemini-3.8-pro', model: 'gemini-3.8-pro', origin: 'cli://agy' }]);
  });
});
