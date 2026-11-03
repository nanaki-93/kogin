// Realistic daemon state for browser tests. Values follow the Go DTOs the
// daemon serves (task states, workflow statuses, gitops integration states,
// finding sources), so the UI is exercised with what it really receives.

import type {
  AgentSettings, AnalysisFileSelection, AnalysisRunFile, ProjectSetup, AnalysisRunSummary, AnalysisStageProgress, AvailableModels, BudgetUsage, ChangeHistoryEntry, ChangeSession, CostEstimate, FileChange, Finding, GitWorkspace, Model, ModelsCurrent, Overview, Project, ReviewProgress, UsageReport, WorkflowPhase,
} from '../../src/api/types';

const now = Date.parse('2026-10-27T10:00:00Z');
export const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString();

export const project: Project = {
  project_id: 'p1',
  project_revision: 'r42',
  name: 'kasuri',
  path: '/work/kasuri',
  type: 'go',
  file_count: 748,
  total_lines: 169_089,
};

const identity = { project_id: project.project_id, project_revision: project.project_revision };

export const indexedPaths = [
  'cmd/daemon/main.go',
  'internal/api/handlers/webui.go',
  'internal/api/handlers/webui_test.go',
  'internal/app/service.go',
  'internal/llm/client.go',
  'README.md',
];

const added = (n: number, text: string) => ({ kind: 'added', new_line: n, text });
const removed = (n: number, text: string) => ({ kind: 'removed', old_line: n, text });
const context = (o: number, n: number, text: string) => ({ kind: 'context', old_line: o, new_line: n, text });

function change(path: string, lines: FileChange['diff']['lines'], summary: string): FileChange {
  return { path, content: '', hash: 'h-' + path, summary, diff: { lines } };
}

/** A task the agents finished: three changed files waiting for human review. */
export function reviewTask(): ChangeSession {
  return {
    ...identity,
    id: 'chg-1',
    kind: 'fix',
    title: 'Reject nested asset paths in the Web UI handler',
    revision: 3,
    hash: 'sha256:9f2c1a7be4',
    state: 'draft',
    freshness: 'current',
    updated_at: ago(6),
    acceptance_criteria: ['Nested asset paths return 404', 'Missing assets return 404 instead of 500'],
    targets: [
      { path: 'internal/api/handlers/webui.go', hash: 'a', exists: true },
      { path: 'internal/api/handlers/webui_test.go', hash: 'b', exists: true },
      { path: 'internal/api/handlers/mime.go', hash: '', exists: false },
    ],
    plan: { approach: 'Reject names with a slash or a parent segment before reading the embedded files.' },
    changes: [
      change('internal/api/handlers/webui.go', [context(41, 41, 'name := trim(r)'), removed(42, 'if name == "" {'), added(42, 'if name == "" || strings.Contains(name, "/") {')], 'Rejects nested names.'),
      change('internal/api/handlers/webui_test.go', [added(88, 'func TestServeAssetRejectsNestedPaths(t *testing.T) {}')], 'Table test for nested names.'),
      change('internal/api/handlers/mime.go', [added(1, 'package handlers')], 'Content types for assets.'),
    ],
    reviewed_files: [],
    file_review_hash: 'sha256:9f2c1a7be4',
    messages: [],
    checks: [
      { name: 'go vet', required: true, state: 'passed', command: ['go', 'vet', './...'], duration_ms: 4200 },
      { name: 'go test', required: true, state: 'passed', command: ['go', 'test', './...'], duration_ms: 18_350 },
      { name: 'staticcheck', required: false, state: 'failed', command: ['staticcheck', './...'], output: 'mime.go:5:6: should not use init', exit_code: 1 },
    ],
    git: { head: '41030b7e', branch: 'orca/reject-nested-a1b2c3' },
    // Every task has its own worktree.
    worktree: { base: '41030b7e', branch: 'orca/reject-nested-a1b2c3' },
    workflow: {
      id: 'wf-1',
      status: 'awaiting_human_review',
      stages: [
        { name: 'create', status: 'completed', started_at: ago(19), finished_at: ago(16), model: stageModel('bug'), usage: spend(2, 18_400, 2_100), cost: estimate(0.061), tools: { calls: 14, turns: 9, finished: true, summary: 'read_file 6, edit_file 4, search 2, run_checks 1, finish 1' } },
        { name: 'test', status: 'completed', started_at: ago(16), finished_at: ago(11), model: stageModel('code'), usage: spend(1, 9_800, 1_300), cost: estimate(0.034) },
        { name: 'review', status: 'completed', started_at: ago(11), finished_at: ago(7), model: stageModel('analyze'), usage: spend(3, 31_000, 1_900), cost: estimate(0.099) },
        { name: 'human_review', status: 'waiting', started_at: ago(7) },
      ],
      review: { proposal_hash: 'sha256:9f2c1a7be4', verdict: 'approve', summary: 'Safe and tested.', findings: [] },
      specialists: [{ id: 'correctness', status: 'completed', evidence: 'model', review: { verdict: 'approve', summary: 'Guard runs first.', findings: [] } }],
      input: { request_id: 'in-1', workflow_id: 'wf-1', status: 'waiting', action: 'approve', expires_at: ago(-60) },
    },
  };
}

/** The same task after the reviewers requested changes: no human input yet. */
export function rejectedTask(): ChangeSession {
  const task = reviewTask();
  const blocking = { title: 'MIME table belongs in its own package', path: 'internal/api/handlers/webui.go', line: 42, detail: 'Move the MIME table into its own package.' };
  const note = { title: 'Consider a table-driven test.' };
  return {
    ...task,
    reviewed_files: task.changes.map(change => change.path),
    workflow: {
      ...task.workflow!,
      status: 'changes_requested',
      input: undefined,
      stages: task.workflow!.stages.map(stage => (stage.name === 'human_review' ? { name: stage.name, status: 'blocked' } : stage)),
      review: { proposal_hash: task.hash, verdict: 'changes_requested', summary: '2 specialist reviews completed; 1 requested changes.', findings: [blocking], notes: [note] },
      specialists: [
        { id: 'correctness', status: 'completed', evidence: 'model_advice', review: { proposal_hash: task.hash, verdict: 'approve', summary: 'Guard runs first.', findings: [], notes: [note] } },
        { id: 'architecture', status: 'completed', evidence: 'model_advice', review: { proposal_hash: task.hash, verdict: 'changes_requested', summary: 'Package boundary.', findings: [blocking] } },
      ],
    },
  };
}

/** The same task paused in its review: the Claude plan reached its usage limit. */
export function pausedTask(): ChangeSession {
  const task = reviewTask();
  const reason = 'The Claude plan reached its usage limit during the review stage. Nothing was lost: resume when the limit resets, or choose another model for that stage.';
  return {
    ...task,
    workflow: {
      ...task.workflow!,
      status: 'paused',
      reason,
      input: undefined,
      review: undefined,
      specialists: undefined,
      // The reset is in the browser's future, whatever the fixture clock says.
      pause: { stage: 'review', resume_from: 'review', provider: 'claude', resumes_at: new Date(Date.now() + 90 * 60_000).toISOString() },
      stages: task.workflow!.stages.map(stage => (stage.name === 'review' ? { ...stage, status: 'paused', reason, finished_at: undefined } : stage.name === 'human_review' ? { name: stage.name, status: 'blocked' } : stage)),
    },
  };
}

/** The model a stage ran with; the daemon records the role's selection as its profile. */
export function stageModel(selection: string): Model {
  return { ...models.scopes[selection], profile: selection };
}

function spend(calls: number, input: number, output: number): BudgetUsage {
  return { model_calls: calls, input_tokens: input, output_tokens: output };
}

function estimate(total: number): CostEstimate {
  return { status: 'estimated', currency: 'USD', price_version: 'fixture-v1', known_subtotal: total, total, reasons: [] };
}

/** Agent usage per task, as GET /api/projects/current/usage reports it. */
export function usage(task: ChangeSession): UsageReport {
  const row = { runs: 4, usage: spend(6, 59_200, 5_300), cost: estimate(0.194) };
  return {
    ...identity,
    currency: 'USD',
    price_version: 'fixture-v1',
    tasks: [
      { session_id: task.id, title: task.title, kind: task.kind, state: task.state, updated_at: task.updated_at, ...row },
      { session_id: 'chg-3', title: 'Cache the parsed index', kind: 'performance', state: 'checked', updated_at: ago(55), runs: 2, usage: spend(3, 22_000, 2_400), cost: estimate(0.071) },
    ],
    other: { runs: 9, usage: spend(14, 120_000, 9_000), cost: { status: 'unknown', currency: 'USD', known_subtotal: 0.31, reasons: ['model_rate_unavailable'] } },
    total: { runs: 15, usage: spend(23, 201_200, 16_700), cost: { status: 'unknown', currency: 'USD', known_subtotal: 0.575, reasons: ['model_rate_unavailable'] } },
  };
}

/** The running stage, and the issue of a task that fixes its issues one at a time, like gitops.ChangeWorkflowPhase. */
function workflowPhase(session: ChangeSession): WorkflowPhase | undefined {
  const workflow = session.workflow;
  const stage = workflow?.stages.find(s => s.status === 'running')?.name;
  if (!workflow || !['running', 'canceling'].includes(workflow.status) || (stage !== 'create' && stage !== 'test' && stage !== 'review')) return undefined;
  const issues = session.issues ?? [];
  const fixing = session.worktree && issues.length > 1 ? issues.findIndex(issue => issue.status === 'fixing') : -1;
  const phase: WorkflowPhase = { stage };
  if (workflow.auto_repair_attempts) phase.repair = workflow.auto_repair_attempts;
  if (workflow.resume_from === 'checks' && !workflow.auto_repair_attempts) phase.checks_only = true;
  if (stage === 'create' && fixing >= 0) Object.assign(phase, { issue: fixing + 1, issues: issues.length });
  return phase;
}

/** Where your review of a version that waits for you stands, like gitops.ChangeReviewProgress. */
function reviewProgress(session: ChangeSession): ReviewProgress | undefined {
  const waits = !session.workflow || session.workflow.status === 'awaiting_human_review';
  if (session.state !== 'draft' || !session.changes.length || !session.hash || !waits) return undefined;
  const viewed = session.file_review_hash === session.hash ? session.changes.filter(change => (session.reviewed_files ?? []).includes(change.path)).length : 0;
  const passed = session.checks.length > 0 && session.checks.every(check => !check.required || check.state === 'passed');
  const step = viewed < session.changes.length ? 'view_files' : passed ? 'accept' : 'run_checks';
  return { step, files: session.changes.length, viewed };
}

export function historyEntry(session: ChangeSession, extra: Partial<ChangeHistoryEntry> = {}): ChangeHistoryEntry {
  return {
    ...identity,
    id: session.id,
    kind: session.kind,
    title: session.title,
    revision: session.revision,
    hash: session.hash,
    state: session.state,
    freshness: session.freshness,
    commit: session.commit,
    updated_at: session.updated_at,
    paths: session.targets.map(target => target.path),
    workflow_status: session.workflow?.status,
    workflow_reason: session.workflow?.reason,
    workflow_phase: workflowPhase(session),
    review: reviewProgress(session),
    finding_targets: session.finding_targets,
    ...extra,
  };
}

export const otherTasks: ChangeHistoryEntry[] = [
  { ...identity, id: 'chg-2', kind: 'feature', title: 'Show per-task model cost', revision: 0, hash: '', state: 'draft', freshness: 'current', updated_at: ago(2), workflow_status: 'running', workflow_phase: { stage: 'create', issue: 2, issues: 3 }, paths: ['internal/app/agent_usage.go'] },
  { ...identity, id: 'chg-3', kind: 'performance', title: 'Cache the parsed index', revision: 2, hash: 'x', state: 'checked', freshness: 'current', updated_at: ago(55), workflow_status: 'failed', workflow_reason: 'go test timed out after 120s', paths: ['internal/project/index.go'] },
  { ...identity, id: 'chg-6', kind: 'fix', title: 'Report the stop reason', revision: 4, hash: 'z', state: 'applied', freshness: 'current', updated_at: ago(3000), commit: { state: 'committed', oid: 'f29fad51' }, paths: ['internal/app/change_pipeline.go'] },
];

export const findings: Finding[] = [
  { id: 'f1', project_id: 'p1', source: 'security_rules', severity: 'high', title: 'Request body read without a size limit', message: 'io.ReadAll on r.Body.', location: { path: 'internal/api/handlers/change_handler.go', start_line: 88, symbol: 'ChangeHandler.Create' }, status: 'open', category: 'security',
    evidence: 'Preconditions: A client can send a request body of any size.\nRemediation: Wrap the body in http.MaxBytesReader before reading it.\nHow to verify: Post a body over the limit and expect 413.' },
  { id: 'f2', project_id: 'p1', source: 'ai', severity: 'medium', title: 'Retry loop ignores cancellation', message: 'time.Sleep without ctx.Done().', location: { path: 'internal/llm/client.go', start_line: 212 }, status: 'open', category: 'bug' },
  { id: 'f3', project_id: 'p1', source: 'vet', severity: 'low', title: 'Unchecked Close error', message: 'A failed Close can lose data.', location: { path: 'internal/storage/store.go', start_line: 64 }, status: 'dismissed', category: 'bug' },
];

export const overview: Overview = {
  ...identity,
  analysis_coverage: { fresh: 402, stale: 21, missing: 8 },
  analysis_run: { status: 'completed', updated_at: ago(180) },
  docs: {
    schema_version: '4',
    status: 'stale',
    overview: {
      purpose: 'A local-first coding assistant: a Go daemon and a browser workspace that run coding agents on a task in its own worktree.',
      architecture: 'cmd/daemon wires internal/api, which calls internal/app; internal/app reads the project through internal/project.',
      languages: [{ name: 'Go', files: 443 }, { name: 'TypeScript', files: 64 }],
      files: 610,
      lines: 77400,
    },
    components: [
      { name: 'Daemon', path: 'cmd/daemon', languages: ['Go'], files: 3, lines: 400, summary: '', responsibilities: [], entry_points: [{ path: 'cmd/daemon/main.go', kind: 'main', detail: 'Go' }], imports: ['internal/api', 'internal/app'], used_by: [] },
      { name: 'Api', path: 'internal/api', languages: ['Go'], files: 40, lines: 6000, summary: 'Decodes local requests and maps errors. It is the only way into the app service.', summary_source: 'module', responsibilities: ['Request policy', 'Error responses'], entry_points: [], imports: ['internal/app'], used_by: ['cmd/daemon'] },
      { name: 'App service', path: 'internal/app', languages: ['Go'], files: 320, lines: 50000, summary: 'Workflow orchestration.', summary_source: 'overview', responsibilities: [], entry_points: [], imports: ['internal/project'], used_by: ['cmd/daemon', 'internal/api'] },
      { name: 'Project', path: 'internal/project', languages: ['Go'], files: 80, lines: 12000, summary: '', responsibilities: [], entry_points: [], imports: [], used_by: ['internal/app'] },
      { name: 'Src', path: 'web/src', languages: ['TypeScript'], files: 64, lines: 9000, summary: '', responsibilities: [], entry_points: [], imports: [], used_by: [] },
    ],
    walkthroughs: [
      { title: 'Serve the Web UI', summary: 'The daemon serves the embedded bundle at /ui.', steps: [
        { text: 'main wires the routes and starts the loopback server.', path: 'cmd/daemon/main.go', symbol: 'main', line: 40 },
        { text: 'The handler returns index.html for client routes and rejects nested asset paths.', path: 'internal/api/handlers/webui.go', symbol: 'ServeAsset', line: 1 },
      ] },
      { title: "Start a task's agents", summary: 'A task runs its agents in its own worktree.', steps: [
        { text: 'The change handler decodes the request.', path: 'internal/api/handlers/change_handler.go', symbol: 'ChangeHandler.Create', line: 88 },
        { text: 'The app service starts the workflow.', path: 'internal/app/service.go' },
      ] },
    ],
    commands: [
      { label: 'Test', command: 'go test ./...', source: 'go.mod' },
      { label: 'Lint', command: 'go vet ./...', source: 'go.mod' },
      { label: 'Test', command: 'npm test', dir: 'web', source: 'web/package.json' },
      { label: 'Dev', command: 'npm run dev', dir: 'web', source: 'web/package.json' },
    ],
    dependencies: [
      { name: 'github.com/odvcencio/gotreesitter', kind: 'Go module', version: 'v0.56.0', source: 'go.mod' },
      { name: 'react', kind: 'npm package', version: '^19.0.0', source: 'web/package.json' },
      { name: 'vite', kind: 'npm package', version: '^6.0.0', dev: true, source: 'web/package.json' },
    ],
    graph: {
      edges: [{ from: 'cmd/daemon', to: 'internal/api', imports: 2 }, { from: 'cmd/daemon', to: 'internal/app', imports: 3 }, { from: 'internal/api', to: 'internal/app', imports: 40 }, { from: 'internal/app', to: 'internal/project', imports: 120 }],
      entry_points: [{ path: 'cmd/daemon/main.go', kind: 'main', detail: 'Go' }, { path: 'web/package.json', kind: 'script', detail: 'dev: vite' }],
      modules: 57,
    },
  },
};

/** A docs component's files, as GET /docs/component lists them. */
export function docsComponentFiles(path: string) {
  const files: Record<string, { path: string; language: string; lines: number; summary?: string }[]> = {
    'internal/api': [
      { path: 'internal/api/error.go', language: 'Go', lines: 120, summary: 'Maps app errors to JSON responses with a user message.' },
      { path: 'internal/api/handlers/webui.go', language: 'Go', lines: 3, summary: 'Serves the embedded Web UI bundle.' },
      { path: 'internal/api/handlers/webui_test.go', language: 'Go', lines: 40 },
    ],
  };
  return { path, files: files[path] ?? [] };
}

const runIdentity = { project_id: identity.project_id, project_revision: identity.project_revision, policy_fingerprint: '3:pol', provider_fingerprint: 'prov', queue_id: 'queue', id: 'analysis-1', generation: 'gen-1' };
const unreachable = 'The model provider could not be reached. Check that it is running and that its address in Settings → Models & agents is right.';
const stage = (name: string, status: string, extra: Partial<AnalysisStageProgress> = {}): AnalysisStageProgress => ({ stage: name, status, attempts: status === 'pending' ? 0 : 1, cached: false, ...extra });

/** An analysis run like the one a daemon without config.yaml leaves: every model stage failed. */
export function analysisRun(status = 'partial'): AnalysisRunSummary {
  const failed = (path: string): AnalysisRunFile => ({ path, language: 'Go', stages: [stage('semantic', 'failed', { reason: unreachable }), stage('performance', 'failed', { reason: unreachable }), stage('security_rules', 'completed_empty', { finding_count: 0 }), stage('security_ai', 'failed', { reason: unreachable })] });
  return {
    identity: runIdentity,
    status,
    files: [
      failed('internal/vault/store.go'),
      failed('internal/vault/crypto.go'),
      { path: 'internal/api/handlers/webui.go', language: 'Go', stages: [stage('semantic', 'completed', { finding_count: 2 }), stage('performance', 'completed_empty', { finding_count: 0 }), stage('security_rules', 'completed', { finding_count: 1 }), stage('security_ai', status === 'running' ? 'running' : 'completed_empty', { finding_count: 0 })] },
      { path: 'README.md', language: 'Text', stages: [stage('semantic', 'skipped', { reason: 'Not eligible for semantic source analysis.', attempts: 0 }), stage('performance', 'completed_empty', { finding_count: 0 }), stage('security_rules', 'skipped', { reason: 'No security rule pack covers this file type.', attempts: 0 }), stage('security_ai', 'completed_empty', { finding_count: 0 })] },
    ],
    sections: [
      { category: 'bugs', status: 'partial', coverage: { total: 3, pending: 0, running: 0, succeeded: 1, partial: 0, failed: 2, skipped: 0, unavailable: 0 }, finding_count: 2 },
      { category: 'performance', status: 'partial', coverage: { total: 4, pending: 0, running: 0, succeeded: 2, partial: 0, failed: 2, skipped: 0, unavailable: 0 }, finding_count: 0 },
      { category: 'security', status: 'partial', coverage: { total: 7, pending: 0, running: 0, succeeded: 4, partial: 0, failed: 2, skipped: 1, unavailable: 0 }, finding_count: 1 },
    ],
    plan: {
      limits: { batch_files: 64, budget_seconds: 1800, max_attempts_per_stage: 2 },
      providers: [{ id: 'p-bug', stages: ['semantic'], model: { scope: 'bug', model: 'local-bug-model', provider_origin: 'http://localhost:1234', remote_provider: false } }],
    },
    checks: status === 'running' ? null : {
      status: 'completed', issues: 3, at: ago(28), checks: [
        { name: 'parse', state: 'passed', issues: 0 },
        { name: 'code budgets', state: 'failed', issues: 1 },
        { name: 'lint', command: ['go', 'vet', './...'], state: 'passed', issues: 0 },
        { name: 'tests', command: ['go', 'test', './...'], state: 'failed', issues: 2 },
      ],
    },
    elapsed_seconds: 139,
    created_at: ago(30),
    updated_at: ago(28),
  };
}

export function gitWorkspace(): GitWorkspace {
  return {
    ...identity,
    available: true,
    root: project.path,
    head: '41030b7e',
    branch: 'main',
    dirty: false,
    conflicts: [],
    worktrees: [
      { path: project.path, head: '41030b7e', branch: 'main', current: true, integration: 'current', sessions: [] },
      { path: '/work/.kasuri/wt/chg-2', head: 'aa11', branch: 'mo/chg-2', integration: 'merged', mergeable: false, sessions: [otherTasks[0]] },
      { path: '/work/.kasuri/wt/chg-7', head: 'bb22', branch: 'mo/chg-7', integration: 'behind', mergeable: true, overlap_paths: ['internal/app/git_merge.go'], sessions: [] },
    ],
  };
}

export const models: ModelsCurrent = {
  scopes: {
    analyze: { scope: 'analyze', model: 'gemini-2.5-pro', provider_origin: 'generativelanguage.googleapis.com', remote_provider: true },
    bug: { scope: 'bug', model: 'qwen3-coder', provider_origin: 'http://127.0.0.1:1234', remote_provider: false },
    code: { scope: 'code', model: 'qwen3-coder', provider_origin: 'http://127.0.0.1:1234', remote_provider: false },
  },
};

// The model catalog: the configured scopes' models plus what Claude Code and the Agy CLI list.
const agyModel = (id: string): Model => ({ scope: '', profile: 'agy:' + id, model: id, provider_origin: 'cli://agy', remote_provider: true, reasoning_effort: 'medium' });
const claudeModel = (id: string): Model => ({ scope: '', profile: 'claude:' + id, model: id, provider_origin: 'cli://claude', remote_provider: true });
export const availableModels: AvailableModels = {
  models: [
    { id: 'configured:' + 'a'.repeat(64), name: 'gemini-2.5-pro', provider: 'gemini', source: 'configured', location: 'remote', model: { ...models.scopes.analyze, scope: '', profile: 'configured:' + 'a'.repeat(64) } },
    { id: 'agy:gemini-3.8-flash-medium', name: 'Gemini 3.8 Flash', provider: 'agy', source: 'agy', location: 'unknown', model: agyModel('gemini-3.8-flash-medium') },
    { id: 'agy:gemini-3.8-pro-high', name: 'Gemini 3.8 Pro', provider: 'agy', source: 'agy', location: 'unknown', model: agyModel('gemini-3.8-pro-high') },
    { id: 'claude:sonnet', name: 'Claude Sonnet', provider: 'anthropic', source: 'claude', location: 'remote', model: claudeModel('sonnet') },
    { id: 'claude:opus', name: 'Claude Opus', provider: 'anthropic', source: 'claude', location: 'remote', model: claudeModel('opus') },
  ],
  defaults: { analyze: 'configured:' + 'a'.repeat(64) },
  pi: { status: 'unavailable', message: 'Pi models could not be loaded. Check that Pi is installed, signed in, and its model configuration is valid, then refresh models.' },
  agy: { status: 'ready' },
  claude: { status: 'ready' },
};

/** The daemon's agent settings for `roles` and `limits`, resolved like the service does. */
export function agentSettings(version: number, roles: Record<string, string> | null, limits: Record<string, number> | null): AgentSettings {
  const ceilings = { model_calls: 40, tool_calls: 120, input_tokens: 400_000, output_tokens: 60_000, elapsed_seconds: 1_800, concurrent_calls: 2, repairs: 2 };
  const effective = roles ?? { creation: 'bug', testing: 'code', review: 'analyze' };
  const resolve = (id: string): Model | undefined => models.scopes[id] ?? availableModels.models.find(choice => choice.id === id)?.model;
  const roleModels: AgentSettings['role_models'] = {};
  for (const role of ['creation', 'testing', 'review'] as const) {
    const model = resolve(effective[role]);
    if (model) roleModels[role] = model;
  }
  return {
    ...project,
    configuration: 'c'.repeat(64),
    version,
    state: roles ? 'overridden' : 'configured',
    roles: effective,
    limits: limits ?? ceilings,
    ceilings,
    models: Object.values(models.scopes),
    role_models: roleModels,
  };
}

export const fileSource = 'package handlers\n\nfunc ServeAsset() {}\n';

/** Project setup as the daemon reports it; `state` decides whether the wizard opens. */
export function projectSetup(state: string): ProjectSetup {
  return {
    ...identity,
    state,
    config_file: '/work/kasuri/.kasuri/config.yaml',
    daemon_config_file: '/home/me/.config/kasuri/config.yaml',
    models: {
      analyze: { provider: '', api_base_url: 'https://api.example/v1', api_key_set: true, model: 'gemini-2.5-pro' },
      bug: { provider: '', api_base_url: 'http://127.0.0.1:1234/v1', api_key_set: false, model: 'qwen3-coder' },
      code: { provider: '', api_base_url: 'http://127.0.0.1:1234/v1', api_key_set: false, model: 'qwen3-coder' },
    },
    roles: { creation: 'bug', testing: 'code', review: 'analyze' },
    limits: { model_calls: 40, tool_calls: 120, input_tokens: 400_000, output_tokens: 60_000, elapsed_seconds: 1_800, concurrent_calls: 2, repairs: 2 },
    timeouts: { import_seconds: 300, analysis_seconds: 120, generation_seconds: 300, workflow_model_seconds: 900, focused_check_seconds: 60 },
  };
}

export function analysisSelection(excluded: string[]): AnalysisFileSelection {
  return {
    ...identity,
    selection_id: 'sel-' + excluded.length,
    excluded_paths: excluded,
    editable: true,
    files: [
      ...indexedPaths.map(path => ({ path, reason: '' })),
      { path: 'vendor/lib/a.go', reason: '' },
      { path: 'vendor/lib/b.go', reason: '' },
      { path: 'assets/logo.png', reason: 'Not a supported text source file.' },
    ],
  };
}
