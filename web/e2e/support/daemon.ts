// A stateful fake of the daemon's loopback API, installed with Playwright
// request interception. It implements the review workflow the UI drives
// (view files, approve, apply, create tasks) with the daemon's guards: every
// mutation must carry the Web UI header and the current proposal identity.

import { test as base, expect, type Page, type Route } from '@playwright/test';
import type { ChangeHistoryEntry, ChangeSession, DaemonEvent, DocsRefresh, Finding, GitWorktree, ProjectSetup } from '../../src/api/types';
import * as data from './fixtures';
import { approveSession, overrideReview, reviseSession, startRun } from './review';

export interface RecordedRequest {
  method: string;
  path: string;
  body: Record<string, unknown> | null;
}

/** The main branch's commits before and after a merge in the fake. */
const MAIN_BEFORE_MERGE = '41030b7e00000000000000000000000000000000';
const MERGED_HEAD = '5e7a9c1d00000000000000000000000000000000';

/** The fake daemon's launch token, written into the document like the daemon's. */
export const WRITE_TOKEN = 'e2e-write-token';

type Handler = (body: Record<string, unknown>, match: RegExpMatchArray) => unknown;

export class FakeDaemon {
  readonly requests: RecordedRequest[] = [];
  readonly sessions = new Map<string, ChangeSession>([['chg-1', data.reviewTask()]]);
  private nextId = 100;
  private readonly overrides = new Map<string, () => unknown>();
  private readonly pending: DaemonEvent[] = [];
  private settings = data.agentSettings(0, null, null);
  private setup = data.projectSetup('configured');
  private selection = data.analysisSelection([]);

  /** Marks the agent overrides as saved for an earlier model configuration. */
  staleSettings(): void {
    this.settings = { ...data.agentSettings(1, { creation: 'code', testing: 'code', review: 'analyze' }, null), state: 'stale' };
  }

  /** Sets the project's setup state for this test; "needed" opens the wizard. */
  serveSetup(state: string): void {
    this.setup = data.projectSetup(state);
  }
  /** The analysis run the overview reports; tests set it with serveRun. */
  private run: ReturnType<typeof data.analysisRun> | undefined;

  serveRun(run: ReturnType<typeof data.analysisRun> | undefined): void {
    this.run = run;
  }

  /** The latest refresh started from the Docs page; tests set it with serveDocsRefresh. */
  private docsRefresh: DocsRefresh | undefined;

  serveDocsRefresh(refresh: DocsRefresh | undefined): void {
    this.docsRefresh = refresh;
  }

  private setRun(status: string) {
    this.run = { ...(this.run ?? data.analysisRun()), status, reason: undefined };
    return this.run;
  }
  /** Console messages a test expects, such as the browser's note on a refused request. */
  readonly expectedConsole: RegExp[] = [];

  /**
   * Adds a live update to every event-stream connection: the fake cannot keep
   * a stream open, and development mode opens the first connection twice.
   */
  push(event: DaemonEvent): void {
    this.pending.push(event);
  }

  /** Replaces a GET route's response for this test. */
  serve(path: string, respond: () => unknown): void {
    this.overrides.set(path, respond);
  }

  mutations(suffix: string): RecordedRequest[] {
    return this.requests.filter(request => request.method !== 'GET' && request.path.endsWith(suffix));
  }

  async install(page: Page): Promise<void> {
    await page.route(url => url.pathname === '/status' || url.pathname.startsWith('/api/'), route => this.handle(route));
    // The daemon writes its launch token into the document it serves; the
    // dev server cannot, so the test writes it once the document is parsed.
    await page.addInitScript(token => {
      document.addEventListener('DOMContentLoaded', () => {
        document.querySelector<HTMLMetaElement>('meta[name="mini-orca-write-token"]')?.setAttribute('content', token);
      });
    }, WRITE_TOKEN);
  }

  private readonly deleted = new Set<string>();
  private readonly extraTasks: ChangeHistoryEntry[] = [];

  /** Adds a listed task, such as a stale one, for this test. */
  addTask(entry: ChangeHistoryEntry): void {
    this.extraTasks.push(entry);
  }

  /** The worktrees besides the main checkout; tasks created in their own worktree add one. */
  private readonly worktrees: GitWorktree[] = data.gitWorkspace().worktrees;
  /** Which worktree each task created in its own worktree lives in. */
  private readonly taskWorktrees = new Map<string, string>();

  /** The tasks of one checkout: the main one ('') or a worktree. */
  private tasks(worktreePath = '') {
    const own = [...this.sessions.values()].filter(session => (this.taskWorktrees.get(session.id) ?? '') === worktreePath).map(session => data.historyEntry(session));
    const others = worktreePath ? [] : [...data.otherTasks, ...this.extraTasks];
    return [...own, ...others].filter(task => !this.deleted.has(task.id));
  }

  /** The Git workspace with each created worktree's tasks, like the daemon reads them. */
  private gitWorkspace() {
    const worktrees = this.worktrees.map(tree => (tree.current || !this.created.has(tree.path) ? tree : { ...tree, sessions: this.tasks(tree.path) }));
    return { ...data.gitWorkspace(), worktrees };
  }

  private readonly created = new Set<string>();

  /** Adds a worktree for this test. */
  addWorktree(tree: GitWorktree): void {
    this.worktrees.push(tree);
  }

  /** Changes a worktree for this test, such as giving it commits the main branch lacks. */
  patchWorktree(path: string, patch: Partial<GitWorktree>): void {
    const index = this.worktrees.findIndex(tree => tree.path === path);
    this.worktrees[index] = { ...this.worktrees[index], ...patch };
  }

  /** Removes a worktree like discard or cleanup-merged: the body names the reviewed head and branch. */
  private discardWorktree(body: Record<string, unknown>) {
    const tree = this.worktrees.find(entry => entry.path === body.path);
    if (!tree || body.source_head !== tree.head || body.source_branch !== tree.branch || body.confirm !== true || body.confirm_execution !== true) throw new Error('worktree branch changed; refresh Git status before discarding');
    // Like the daemon: its work goes only when asked, and only from a worktree without tasks.
    const work = body.discard_work === true;
    if (tree.dirty && !work) throw new Error("commit the worktree's remaining changes before merging or discarding it");
    if (work && this.tasks(tree.path).length) throw new Error("delete the worktree's tasks before discarding its work");
    this.worktrees.splice(this.worktrees.indexOf(tree), 1);
    return { state: 'discarded', path: tree.path, removed_worktree: tree.path, removed_branch: work || tree.integration === 'merged' ? tree.branch : undefined, warnings: [] };
  }

  /** Merges a mergeable worktree; keep_worktree keeps it, now merged, with its branch. */
  private mergeWorktree(body: Record<string, unknown>) {
    const tree = this.worktrees.find(entry => entry.path === body.path);
    if (!tree?.mergeable || body.source_head !== tree.head) throw new Error('choose an existing worktree from this repository');
    this.worktrees.splice(this.worktrees.indexOf(tree), 1);
    const merged = { state: 'merged', previous_head: MAIN_BEFORE_MERGE, repository: { head: MERGED_HEAD, branch: 'main' }, index: { project_id: data.project.project_id, project_revision: data.project.project_revision }, warnings: [] };
    if (body.keep_worktree === true) {
      this.worktrees.push({ ...tree, integration: 'merged', mergeable: false, overlap_paths: undefined });
      return merged;
    }
    return { ...merged, removed_worktree: tree.path, removed_branch: tree.branch };
  }

  /** What the checks after a merge report; tests choose. */
  mergeChecksOutcome: 'passed' | 'failed' = 'passed';

  /** Checks a merge like the daemon: the commits must be the ones the merge reported. */
  private checkMerge(body: Record<string, unknown>) {
    if (body.from !== MAIN_BEFORE_MERGE || body.to !== MERGED_HEAD) throw new Error('the checkout is no longer on the merged commit');
    const failed = this.mergeChecksOutcome === 'failed';
    const tests = failed
      ? { name: 'tests', required: true, state: 'failed', command: ['go', 'test', './...'], output: 'New failures: example/app.TestServe. Already failing before this change: example/app.TestLegacy.', duration_ms: 2100 }
      : { name: 'tests', required: true, state: 'passed', outcome: 'known_failures', command: ['go', 'test', './...'], output: 'Only failures that already happened before this change (example/app.TestLegacy); they do not block this task.', duration_ms: 2100 };
    return {
      from: body.from,
      to: body.to,
      outcome: this.mergeChecksOutcome,
      summary: failed ? 'Failed on the merged result: tests. The merge stays in place: fix it in a new task, or revert the merge commit.' : 'The tests and lint of the merged subprojects pass on the merged result.',
      changed_paths: 2,
      checks: [tests, { name: 'lint', required: true, state: 'passed', command: ['go', 'vet', './...'], duration_ms: 800 }, { name: 'other subprojects', required: false, state: 'skipped', outcome: 'not_affected', output: 'Not run, since no changed file is in them: web.' }],
    };
  }

  /** Deletes a task like the daemon: the body names the version shown; running tasks are kept. */
  private deleteTask(id: string, body: Record<string, unknown>) {
    const task = [...this.tasks(), ...[...this.created].flatMap(path => this.tasks(path))].find(entry => entry.id === decodeURIComponent(id));
    if (!task) throw new Error('This task no longer exists; refresh the task list.');
    if (body.revision !== task.revision || body.hash !== task.hash) throw new Error('The task changed since it was loaded.');
    if (task.workflow_status === 'running') throw new Error('This task is in use: stop its agents before deleting it.');
    this.deleted.add(task.id);
    this.sessions.delete(task.id);
    return { deleted: task.id };
  }

  /** Marks a task's files as changed on the main branch since it started, as merging another task does. */
  overtake(id: string, paths: string[]): void {
    this.sessions.set(id, { ...this.sessions.get(id)!, main_changed: { branch: 'main', head: MERGED_HEAD, paths } });
  }

  /** What retaking an overtaken task decides; tests choose. */
  retakeOutcome: 'still_valid' | 'not_valid' = 'still_valid';

  /** Retakes like the daemon: the old task goes, and a valid one starts again in a new worktree. */
  private retake(id: string, body: Record<string, unknown>) {
    const session = this.sessions.get(decodeURIComponent(id));
    if (!session) throw new Error('This task no longer exists; refresh the task list.');
    if (body.revision !== session.revision || body.hash !== session.hash) throw new Error('The task changed since it was loaded.');
    if (!session.main_changed) throw new Error('only a task in its own worktree whose files the main branch changed since it started is retaken');
    if (body.confirm_execution !== true) throw new Error('confirm the Git action and execution of repository filters or merge drivers');
    this.deleted.add(session.id);
    this.sessions.delete(session.id);
    if (this.retakeOutcome === 'not_valid') return { outcome: 'not_valid', reason: 'The merged change already rejects nested paths.', deleted: session.id, dismissed_issues: (session.finding_targets ?? []).length, warnings: [] };
    const request = { kind: session.kind, title: session.title, paths: session.targets.map(target => target.path), acceptance_criteria: session.acceptance_criteria, finding_targets: session.finding_targets, worktree: true, confirm_execution: true };
    return { outcome: 'still_valid', reason: 'ServeAsset still accepts nested paths.', opened: this.createTask(request, 'd4e5f6'), deleted: session.id, warnings: [] };
  }

  /** What re-analyzing a stale task decides; tests choose. */
  reanalysisOutcome: 'still_valid' | 'not_valid' = 'still_valid';

  /** Re-analyzes like the daemon: only a stale task; valid ones are captured again, the rest deleted. */
  private reanalyze(id: string, body: Record<string, unknown>) {
    const session = this.sessions.get(decodeURIComponent(id));
    if (!session) throw new Error('This task no longer exists; refresh the task list.');
    if (body.revision !== session.revision || body.hash !== session.hash) throw new Error('The task changed since it was loaded.');
    if (session.freshness === 'current') throw new Error('this task matches the current files; only an out-of-date task is re-analyzed');
    if (this.reanalysisOutcome === 'not_valid') {
      this.deleted.add(session.id);
      this.sessions.delete(session.id);
      return { outcome: 'not_valid', reason: 'ServeAsset now rejects nested paths.', deleted: session.id, dismissed_issues: (session.finding_targets ?? []).length, warnings: [] };
    }
    const reason = 'ServeAsset still accepts nested paths.';
    const refreshed: ChangeSession = { ...session, revision: session.revision + 1, hash: 'sha256:empty', freshness: 'current', stale_reason: undefined, changes: [], checks: [], reviewed_files: [], reviewed_hash: undefined, comments: [], workflow: undefined, messages: [{ role: 'assistant', content: `Re-analyzed against the current files: the issue is still present. ${reason}` }] };
    this.sessions.set(session.id, refreshed);
    return { outcome: 'still_valid', reason, session: refreshed, warnings: [] };
  }

  private readonly gets: [RegExp, () => unknown][] = [
    [/^\/status$/, () => ({ status: 'ok', version: '9.9.9' })],
    [/^\/api\/models\/current$/, () => data.models],
    [/^\/api\/models\/available$/, () => data.availableModels],
    [/^\/api\/projects\/current$/, () => data.project],
    [/^\/api\/projects\/current\/index$/, () => ({ ...data.project, files: data.indexedPaths.map(path => ({ path, content_hash: 'h', language: 'go', line_count: 3 })) })],
    [/^\/api\/projects\/current\/execution-trust$/, () => ({ ...data.project, trusted: true, commands: [['go', 'test', './...']] })],
    [/^\/api\/projects\/current\/git\/workspace$/, () => this.gitWorkspace()],
    [/^\/api\/projects\/current\/changes$/, () => this.tasks()],
    [/^\/api\/projects\/current\/changes\/recovery$/, () => ({ ...data.project, session_id: '', state: '', hash: '', undo_available: false, warnings: [] })],
    [/^\/api\/projects\/current\/findings$/, () => this.findings()],
    [/^\/api\/projects\/current\/rules$/, () => ({ ...data.project, test_patterns: ['*_test.go', '*.test.ts', '**/tests/**'], guides: ['AGENTS.md'], problems: [] })],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/rules$/, () => ({ session_id: 'chg-1', revision: 3, files: [], problems: [] })],
    [/^\/api\/projects\/current\/overview$/, () => ({ ...data.overview, analysis_run: this.run ?? data.overview.analysis_run, docs_refresh: this.docsRefresh })],
    [/^\/api\/projects\/current\/agent-settings$/, () => this.settings],
    [/^\/api\/projects\/current\/setup$/, () => this.setup],
    [/^\/api\/projects\/current\/analysis\/selection$/, () => this.selection],
    [/^\/api\/projects\/current\/usage$/, () => data.usage(this.sessions.get('chg-1')!)],
    [/^\/api\/projects\/current\/quarantine$/, () => ({ files: [], skipped: [] })],
    [/^\/api\/projects\/current\/files\/info$/, () => ({ path: 'internal/api/handlers/webui.go', content_hash: 'h', language: 'Go', line_count: 3, content: data.fileSource })],
    [/^\/api\/projects\/current\/files\/analysis$/, () => ({ path: 'internal/api/handlers/webui.go', status: 'fresh', purpose: 'Serves the embedded Web UI bundle.', responsibilities: ['Return index.html for client routes'], side_effects: ['Reads the embedded files'], generated_at: new Date().toISOString() })],
  ];

  private readonly posts: [RegExp, Handler][] = [
    [/^\/api\/projects\/current\/changes$/, body => this.createTask(body)],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/review-file$/, (body, m) => this.update(m[1], body, s => ({ ...s, reviewed_files: [...new Set([...(s.reviewed_files ?? []), String(body.path)])], file_review_hash: s.hash }))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/workflow\/input$/, (body, m) => this.update(m[1], body, s => ({ ...s, workflow: { ...s.workflow!, input: { ...s.workflow!.input!, status: String(body.decision) === 'approve' ? 'answered' : 'rejected' }, status: body.decision === 'approve' ? 'awaiting_human_review' : 'canceled' } }))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/review$/, (body, m) => this.update(m[1], body, s => approveSession(s, body))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/review-override$/, (body, m) => this.update(m[1], body, s => overrideReview(s, body))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/comments$/, (body, m) => this.update(m[1], body, s => this.comment(s, body))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/comments\/delete$/, (body, m) => this.update(m[1], body, s => ({ ...s, comments: (s.comments ?? []).filter(c => c.id !== body.id || c.sent_revision) }))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/messages$/, (body, m) => this.update(m[1], body, s => reviseSession(s, body))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/workflow$/, (body, m) => this.update(m[1], body, s => startRun(s, body))],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/apply$/, (body, m) => this.apply(m[1], body)],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/delete$/, (body, m) => this.deleteTask(m[1], body)],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/reanalyze$/, (body, m) => this.reanalyze(m[1], body)],
    [/^\/api\/projects\/current\/changes\/([^/]+)\/retake$/, (body, m) => this.retake(m[1], body)],
    [/^\/api\/projects\/current\/agent-settings\/control$/, body => this.controlSettings(body)],
    [/^\/api\/projects\/current\/setup$/, body => this.saveSetup(body)],
    [/^\/api\/projects\/current\/setup\/timeouts$/, body => (this.setup = { ...this.setup, state: 'configured', timeouts: body.timeouts as ProjectSetup['timeouts'] })],
    [/^\/api\/projects\/current\/setup\/skip$/, () => (this.setup = { ...this.setup, state: 'skipped' })],
    [/^\/api\/projects\/current\/setup\/test-model$/, body => ((body.model as { model?: string }).model ? { ok: true, model: (body.model as { model: string }).model, latency_ms: 840 } : { ok: false, latency_ms: 0, reason: 'The model provider could not be reached.' })],
    [/^\/api\/projects\/current\/analysis\/selection$/, body => (this.selection = { ...data.analysisSelection(body.excluded_paths as string[]) })],
    [/^\/api\/projects\/current\/analysis\/preview$/, body => ({ identity: { ...body, queue_id: 'queue-2' }, preview_id: 'preview-1', providers: [{ id: 'p-bug', model: { model: 'local-bug-model', provider_origin: 'http://localhost:1234' } }], files: [{}, {}, { retained: true }, { retained: true }], max_model_requests: 12, security_review_intent_required: false,
      checks: { commands: [{ name: 'lint', kind: 'lint', command: ['go', 'vet', './...'] }, { name: 'tests: web', kind: 'test', dir: 'web', command: ['npm', 'test'] }] } })],
    [/^\/api\/projects\/current\/analysis\/run$/, () => this.setRun('running')],
    [/^\/api\/projects\/current\/ask$/, body => ({ project_id: body.project_id, project_revision: body.project_revision, question: body.question, answer: 'Assets are served from the embedded bundle; a nested path is rejected before the file is read.', citations: [{ path: 'internal/api/handlers/webui.go', start_line: 1, end_line: 3, quote: 'func ServeAsset() {}', content_hash: 'sha256:abc' }], model: 'claude:opus', turns: 4, tool_calls: 7, at: new Date().toISOString() })],
    [/^\/api\/projects\/current\/docs\/refresh$/, () => (this.docsRefresh = { status: 'running', components: 0, summarized: 0, kept: 0, overview: 'skipped', at: new Date().toISOString() })],
    [/^\/api\/projects\/current\/analysis\/run\/control$/, body => this.setRun(body.action === 'pause' ? 'paused' : body.action === 'cancel' ? 'canceled' : 'running')],
    [/^\/api\/projects\/current\/git\/merge-preview$/, () => ({ commits: 'bb22 Merge preview lists overlapping files', diff: '' })],
    [/^\/api\/projects\/current\/git\/merge$/, body => this.mergeWorktree(body)],
    [/^\/api\/projects\/current\/git\/merge-checks$/, body => this.checkMerge(body)],
    [/^\/api\/projects\/current\/git\/discard-worktree$/, body => this.discardWorktree(body)],
    [/^\/api\/projects\/current\/git\/cleanup-merged$/, body => ({ ...this.discardWorktree(body), state: 'merged' })],
  ];

  private async handle(route: Route): Promise<void> {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method();
    const body = method === 'GET' ? null : ((request.postDataJSON() ?? {}) as Record<string, unknown>);
    this.requests.push({ method, path: url.pathname, body });

    if (url.pathname.endsWith('/events')) {
      const frames = this.pending.map(event => `event: update\ndata: ${JSON.stringify(event)}\n\n`).join('');
      return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' + frames });
    }
    if (request.headers()['x-mini-orca-web-ui'] !== '1') {
      return json(route, 403, { user_message: 'Missing the Web UI request marker.' });
    }
    if (method !== 'GET' && request.headers()['x-mini-orca-token'] !== WRITE_TOKEN) {
      return json(route, 401, { user_message: 'Reload the Web UI to get this daemon\'s write token.' });
    }
    try {
      if (method === 'GET') {
        const override = this.overrides.get(url.pathname);
        if (override) return json(route, 200, override());
        const session = url.pathname.match(/^\/api\/projects\/current\/changes\/([^/]+)$/);
        if (session && this.sessions.has(decodeURIComponent(session[1]))) return json(route, 200, this.sessions.get(decodeURIComponent(session[1])));
        if (url.pathname === '/api/projects/current/changes' && url.searchParams.get('worktree_path')) return json(route, 200, this.tasks(url.searchParams.get('worktree_path')!));
        const issue = url.pathname.match(/^\/api\/projects\/current\/findings\/([^/]+)\/task$/);
        if (issue) return json(route, 200, this.issueTask(decodeURIComponent(issue[1])));
        const found = this.gets.find(([pattern]) => pattern.test(url.pathname));
        return found ? json(route, 200, found[1]()) : json(route, 404, { message: `no fake for GET ${url.pathname}` });
      }
      for (const [pattern, handler] of this.posts) {
        const match = url.pathname.match(pattern);
        if (match) return json(route, 200, handler(body ?? {}, match));
      }
      return json(route, 404, { message: `no fake for ${method} ${url.pathname}` });
    } catch (err) {
      return json(route, 409, { user_message: (err as Error).message });
    }
  }

  /**
   * Drafts a task for a listed issue like the daemon: its file and that
   * file's Go test, a kind from the category, and the criteria for the kind.
   */
  private issueTask(id: string) {
    const listed = (this.overrides.get('/api/projects/current/findings')?.() ?? { findings: data.findings }) as { findings: Finding[] };
    const finding = listed.findings.find(entry => entry.id === id);
    if (!finding) throw new Error('the issue is no longer listed; refresh the issues');
    const file = finding.location?.path || finding.task_spec?.target_path || '';
    const refactor = finding.category === 'maintainability';
    const paths = file ? [file] : [];
    if (file.endsWith('.go') && !file.endsWith('_test.go')) paths.push(file.replace(/\.go$/, '_test.go'));
    return {
      kind: finding.category === 'security' ? 'security' : finding.category === 'performance' ? 'performance' : 'fix',
      title: refactor ? finding.title : `Fix: ${finding.title}`,
      description: `${finding.message}${finding.evidence ? `\n\nEvidence:\n${finding.evidence}` : ''}${file ? `\n\nLocation: ${file}${finding.location?.start_line ? `:${finding.location.start_line}` : ''}` : ''}`,
      paths,
      acceptance_criteria: finding.task_spec?.acceptance_criteria?.length
        ? finding.task_spec.acceptance_criteria
        : refactor
          ? [`Bring ${file} within the budgets of its AGENTS.md, splitting it into cohesive sibling files where needed`, 'Keep its behavior and public interface; existing tests pass unchanged']
          : [`Resolve “${finding.title}” without breaking existing callers`, 'Add a regression test that fails before the fix'],
      finding_targets: finding.project_id && finding.location?.path ? [{ project_id: finding.project_id, kind: 'semantic', id: finding.id, path: finding.location.path }] : undefined,
    };
  }

  /** Saves setup like the daemon: models, roles, budget and time limits; secrets are never echoed. */
  private saveSetup(body: Record<string, unknown>) {
    if (body.project_revision !== this.setup.project_revision) throw new Error('The project changed; reload setup.');
    const models = body.models as ProjectSetup['models'];
    const echoed = Object.fromEntries(Object.entries(models).map(([scope, model]) => [scope, { ...model, api_key: undefined, api_key_set: Boolean(model.api_key) || Boolean(model.api_key_set) }]));
    this.setup = { ...this.setup, state: 'configured', models: echoed as ProjectSetup['models'], roles: body.roles as ProjectSetup['roles'], limits: body.limits as ProjectSetup['limits'], timeouts: (body.timeouts as ProjectSetup['timeouts']) ?? this.setup.timeouts };
    // Saving replaces agent overrides, like the daemon.
    const overridesReset = this.settings.state !== 'configured';
    if (overridesReset) this.settings = data.agentSettings(this.settings.version! + 1, null, null);
    return { ...this.setup, overrides_reset: overridesReset || undefined };
  }

  /** Replaces or resets agent settings with the daemon's version and configuration guard. */
  private controlSettings(body: Record<string, unknown>) {
    const current = this.settings;
    if (body.confirm !== true || body.version !== current.version || body.configuration !== current.configuration || body.project_revision !== current.project_revision) {
      throw new Error('Agent settings changed; reload before editing.');
    }
    if (body.action === 'reset') {
      this.settings = data.agentSettings(current.version! + 1, null, null);
      return this.settings;
    }
    const values = body.values as { roles: Record<string, string>; limits: Record<string, number>; disabled_skills: string[] } | undefined;
    if (body.action !== 'replace' || !values || !Array.isArray(values.disabled_skills)) throw new Error('Replace requires complete roles, limits and disabled skills.');
    this.settings = data.agentSettings(current.version! + 1, values.roles, values.limits);
    return this.settings;
  }

  /** Applies a guarded update: the request must name the current revision and hash. */
  private update(id: string, body: Record<string, unknown>, next: (session: ChangeSession) => ChangeSession): ChangeSession {
    const session = this.sessions.get(decodeURIComponent(id));
    if (!session) throw new Error('Task not found.');
    if (body.revision !== session.revision || body.hash !== session.hash || body.project_revision !== session.project_revision) {
      throw new Error('The proposal changed since it was loaded.');
    }
    const updated = next(session);
    this.sessions.set(session.id, updated);
    return updated;
  }

  /** A comment anchors to a line of the current proposal and quotes it. */
  private comment(s: ChangeSession, body: Record<string, unknown>): ChangeSession {
    const edit = s.changes.find(change => change.path === body.path);
    const line = edit?.diff.lines.find(l => (body.side === 'old' ? l.kind === 'removed' && l.old_line === body.line : l.kind !== 'removed' && l.new_line === body.line));
    if (!line || typeof body.text !== 'string' || !body.text.trim()) throw new Error('Comment on a line of the current proposal.');
    const comment = { id: `cm-${this.nextId++}`, path: String(body.path), side: body.side as 'new' | 'old', line: Number(body.line), excerpt: line.text, text: body.text.trim(), hash: s.hash, created_at: data.ago(0) };
    return { ...s, comments: [...(s.comments ?? []), comment] };
  }

  private apply(id: string, body: Record<string, unknown>) {
    if (body.confirm !== true) throw new Error('Apply needs explicit confirmation.');
    const updated = this.update(id, body, s => {
      if (s.reviewed_hash !== s.hash) throw new Error('Approve this version first.');
      return { ...s, state: 'applied', commit: body.commit_message ? { state: 'committed', oid: 'c0ffee12', message: String(body.commit_message) } : undefined };
    });
    return { ...data.project, session_id: updated.id, state: 'applied', hash: updated.hash, undo_available: !updated.worktree, warnings: [], commit: updated.commit };
  }

  /** The issues as the daemon reports them: an open one a draft task links is in progress. */
  private findings() {
    const fixing = new Set([...this.sessions.values()].filter(session => session.state === 'draft').flatMap(session => (session.finding_targets ?? []).map(target => target.id)));
    return { ...data.project, findings: data.findings.map(finding => (finding.status === 'open' && fixing.has(finding.id) ? { ...finding, status: 'in_progress' } : finding)) };
  }

  /** Creates a task like the daemon; with worktree, in a new branch and worktree of its own. */
  private createTask(body: Record<string, unknown>, suffix = 'a1b2c3'): ChangeSession {
    const id = `chg-${this.nextId++}`;
    if (body.worktree === true && body.confirm_execution !== true) throw new Error('confirm the Git action and execution of repository filters or merge drivers');
    const paths = (body.paths as string[] | undefined) ?? [];
    const session: ChangeSession = {
      ...data.project,
      id,
      kind: String(body.kind ?? 'fix'),
      title: String(body.title ?? ''),
      revision: 0,
      hash: '',
      state: 'draft',
      freshness: 'current',
      updated_at: data.ago(0),
      acceptance_criteria: (body.acceptance_criteria as string[] | undefined) ?? [],
      targets: paths.map(path => ({ path, hash: '', exists: data.indexedPaths.includes(path) })),
      finding_targets: body.finding_targets as ChangeSession['finding_targets'],
      changes: [],
      messages: [],
      checks: [],
    };
    this.sessions.set(id, session);
    if (body.worktree !== true) return session;
    const name = `${taskSlug(String(body.title))}-${suffix}`;
    const path = `${data.project.path}/.worktrees/${name}`;
    session.worktree = { base: '41030b7e', branch: `orca/${name}` };
    this.taskWorktrees.set(id, path);
    this.created.add(path);
    this.worktrees.push({ path, head: '41030b7e', branch: `orca/${name}`, integration: 'merged', mergeable: false, dirty: false, sessions: [] });
    return { ...session, worktree_path: path, branch: `orca/${name}`, warnings: [] } as ChangeSession;
  }
}

/** The daemon's branch name for a task: its first words, within 24 characters. */
function taskSlug(title: string): string {
  let slug = '';
  for (const word of title.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean)) {
    const next = slug ? `${slug}-${word}` : word;
    if (next.length > 24) return slug || word.slice(0, 24);
    slug = next;
  }
  return slug || 'task';
}

function json(route: Route, status: number, payload: unknown) {
  return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(payload) });
}

/** Tests get a fake daemon and fail on any console error or uncaught exception. */
export const test = base.extend<{ daemon: FakeDaemon }>({
  daemon: [
    async ({ page }, use) => {
      const daemon = new FakeDaemon();
      await daemon.install(page);
      const errors: string[] = [];
      page.on('pageerror', error => errors.push(String(error)));
      page.on('console', message => {
        if (message.type() === 'error') errors.push(message.text());
      });
      await use(daemon);
      expect(errors.filter(error => !daemon.expectedConsole.some(pattern => pattern.test(error))), 'browser console errors').toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
