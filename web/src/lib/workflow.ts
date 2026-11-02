// Agent workflow presentation and request shaping. Consent is captured per
// request: every remote destination the run will use is listed and confirmed.

import type { AgentSettings, BudgetUsage, ChangeHistoryEntry, ChangeSession, ChangeWorkflow, CostEstimate, GitWorkspace, GitWorktree, ModelsCurrent, ProjectIdentity, ReviewItem, StageTools, WorkflowPause } from '@/api/types';
import { durationBetween, plural, type Tone } from './util';
import { phaseTone, type PhaseTone } from './readiness';

export const STAGE_LABELS: Record<string, string> = {
  create: 'Write the change',
  test: 'Write and run tests',
  review: 'Specialist review',
  human_review: 'Your review',
};

export const STAGE_ACTORS: Record<string, string> = {
  create: 'Creation agent',
  test: 'Testing agent',
  review: 'Reviewers',
  human_review: 'You',
};

export const SPECIALIST_LABELS: Record<string, string> = {
  correctness: 'Correctness',
  architecture: 'Architecture',
  test_quality: 'Test quality',
  security: 'Security',
  performance: 'Performance',
};

export interface StageView {
  name: string;
  label: string;
  actor: string;
  status: string;
  tone: PhaseTone;
  reason: string;
  model: string;
  durationMs: number;
  startedAt?: string;
  usage?: BudgetUsage;
  cost?: CostEstimate;
  /** How the stage's agent worked with tools, for a tool-using run. */
  tools?: string;
}

/** "Worked with tools: 34 calls in 12 turns (read_file 12, …)", or "" for a one-shot stage. */
export function stageToolsLabel(tools: StageTools | undefined): string {
  if (!tools) return '';
  const counts = tools.summary ? ` (${tools.summary})` : '';
  const ended = tools.finished ? '' : '; stopped before finishing';
  return `Worked with tools: ${plural(tools.calls, 'call')} in ${plural(tools.turns, 'turn')}${counts}${ended}`;
}

export function workflowStages(workflow: ChangeWorkflow | undefined): StageView[] {
  // A checks start keeps the tests: its test stage only runs the checks,
  // until a repair has the testing agent write them again.
  const checksOnly = workflow?.resume_from === 'checks' && !workflow.auto_repair_attempts;
  return (workflow?.stages ?? []).map(stage => ({
    name: stage.name,
    label: checksOnly && stage.name === 'test' ? 'Run the checks' : STAGE_LABELS[stage.name] ?? stage.name,
    actor: checksOnly && stage.name === 'test' ? 'Checks' : STAGE_ACTORS[stage.name] ?? '',
    status: stage.status || 'pending',
    tone: phaseTone(stage.status),
    reason: stage.reason ?? '',
    model: checksOnly && stage.name === 'test' ? '' : stage.model?.model ?? '',
    durationMs: durationBetween(stage.started_at, stage.finished_at),
    startedAt: stage.started_at,
    usage: stage.usage,
    cost: stage.cost,
    tools: stageToolsLabel(stage.tools),
  }));
}

/**
 * The reviewers' overall verdict: the daemon's aggregate, else derived from the
 * specialists. A verdict on an earlier version, given with `hash`, is 'none'.
 */
export function reviewVerdict(workflow: ChangeWorkflow | undefined, hash?: string): 'approve' | 'changes_requested' | 'pending' | 'none' {
  if (!workflow) return 'none';
  if (hash !== undefined && workflow.review && workflow.review.proposal_hash !== hash) return 'none';
  const aggregate = workflow.review?.verdict;
  if (aggregate === 'approve' || aggregate === 'changes_requested') return aggregate;
  const specialists = workflow.specialists ?? [];
  if (specialists.some(sp => sp.review?.verdict === 'changes_requested' || sp.review?.verdict === 'reject')) return 'changes_requested';
  if (specialists.length && specialists.every(sp => sp.review?.verdict === 'approve')) return 'approve';
  return 'pending';
}

/** The human override of the reviewers' rejection, when it covers this exact hash. */
export function activeOverride(workflow: ChangeWorkflow | undefined, hash: string | undefined) {
  const override = workflow?.override;
  return override && hash && override.proposal_hash === hash && workflow?.review?.proposal_hash === hash ? override : undefined;
}

const RUN_LABELS: Record<string, string> = {
  queued: 'Queued',
  running: 'Working',
  canceling: 'Stopping',
  awaiting_human_review: 'Finished',
  changes_requested: 'Finished with requested changes',
  failed: 'Failed',
  canceled: 'Stopped',
  interrupted: 'Interrupted',
  stale: 'Outdated',
  outdated: 'Not run on this version',
  paused: 'Paused at a plan limit',
};

/** Whether the agents finished with this version: a manual proposal has no run to wait for. */
export function agentsFinished(session: { changes?: unknown[] | null; workflow?: ChangeWorkflow }): boolean {
  if (!(session.changes ?? []).length) return false;
  return !session.workflow || ['awaiting_human_review', 'changes_requested'].includes(session.workflow.status);
}

/** How far the agent run got, worded for the review gate. */
export function agentRunLabel(workflow: ChangeWorkflow | undefined, hasChanges: boolean): string {
  if (!workflow) return hasChanges ? 'Manual proposal' : 'Not started';
  return RUN_LABELS[workflow.status] ?? workflow.status.replace(/_/g, ' ');
}

/** A short human label for a stage that has no duration yet. */
export function stageStatusLabel(stage: Pick<StageView, 'name' | 'status' | 'tone'>): string {
  if (stage.name === 'human_review' && (stage.status === 'waiting' || stage.status === 'awaiting_human_review')) return 'Waiting for you';
  if (stage.tone === 'running') return 'Running…';
  return (stage.status || 'pending').replace(/_/g, ' ').replace(/^./, c => c.toUpperCase());
}

export interface SpecialistView {
  id: string;
  label: string;
  status: string;
  tone: PhaseTone;
  verdict: string;
  summary: string;
  findings: ReviewItem[];
  notes: ReviewItem[];
  evidence: string;
  reason: string;
  model: string;
}

export function workflowSpecialists(workflow: ChangeWorkflow | undefined): SpecialistView[] {
  return (workflow?.specialists ?? []).map(item => ({
    id: item.id,
    label: SPECIALIST_LABELS[item.id] ?? item.id,
    status: item.status || 'pending',
    tone: phaseTone(item.review?.verdict ?? item.status),
    verdict: item.review?.verdict ?? '',
    summary: item.review?.summary ?? '',
    findings: item.review?.findings ?? [],
    notes: item.review?.notes ?? [],
    evidence: item.evidence ?? '',
    reason: item.reason ?? '',
    model: item.model?.model ?? '',
  }));
}

/** Where a finding leads: the task's diff of its file at its line, or the file in Code when the proposal does not change it. */
export function reviewItemRoute(session: Pick<ChangeSession, 'id' | 'changes'>, item: ReviewItem): { path: string; params: Record<string, string | undefined> } | null {
  if (!item.path) return null;
  const line = item.line ? String(item.line) : undefined;
  if ((session.changes ?? []).some(change => change.path === item.path)) return { path: `/tasks/${session.id}/files`, params: { file: item.path, line } };
  return { path: '/code', params: { file: item.path, line } };
}

/** A finding's place as it reads in a list: the file's name and the line. */
export function reviewItemPlace(item: ReviewItem): string {
  if (!item.path) return '';
  const name = item.path.slice(item.path.lastIndexOf('/') + 1);
  return item.line ? `${name}:${item.line}` : name;
}

export const DEFAULT_ROLES = { creation: 'bug', testing: 'code', review: 'analyze' } as const;

export interface Destination {
  stage: string;
  selection: string;
  model: string;
  origin: string;
}

export interface DestinationChoice {
  selections: { create: string; test: string; review: string };
  destinations: Destination[];
  confirmed: string[];
  local: { stage: string; model: string }[];
}

/** Resolves role selections and which of them reach a remote provider. */
export function workflowDestinations(settings: AgentSettings | null | undefined, models: ModelsCurrent | null | undefined): DestinationChoice {
  const roles = { ...DEFAULT_ROLES, ...(settings?.roles ?? {}) };
  const selections = { create: roles.creation, test: roles.testing, review: roles.review };
  const scopes = models?.scopes ?? {};
  const catalog = settings?.models ?? [];
  const destinations: Destination[] = [];
  const local: { stage: string; model: string }[] = [];
  const confirmed: string[] = [];
  const roleOf = { create: 'creation', test: 'testing', review: 'review' } as const;
  for (const stage of ['create', 'test', 'review'] as const) {
    const selection = selections[stage];
    const model = settings?.role_models?.[roleOf[stage]] ?? scopes[selection] ?? catalog.find(entry => entry.scope === selection);
    if (model?.remote_provider) {
      destinations.push({ stage, selection, model: model.model || selection, origin: model.provider_origin || 'remote provider' });
      if (!confirmed.includes(selection)) confirmed.push(selection);
    } else {
      local.push({ stage, model: model?.model || selection });
    }
  }
  return { selections, destinations, confirmed, local };
}

export function workflowRequest(identity: ProjectIdentity & { revision: number; hash: string }, message: string, choice: DestinationChoice, options: { autoRepair?: boolean; confirmSecurity?: boolean; planHash?: string; includeComments?: boolean; start?: RunStart } = {}) {
  return {
    ...identity,
    message,
    models: choice.selections,
    confirmed_profiles: choice.confirmed,
    confirm_security: Boolean(options.confirmSecurity),
    auto_repair: Boolean(options.autoRepair),
    plan_hash: options.planHash || undefined,
    include_comments: options.includeComments || undefined,
    resume_from: options.start && options.start !== 'create' ? options.start : undefined,
  };
}

/** Where a run of the agents starts on a task that already has a proposal. */
export type RunStart = 'review' | 'checks' | 'test' | 'create';

export interface RunStartOption {
  value: RunStart;
  label: string;
  detail: string;
  /** Why the daemon would refuse this start, or ''. */
  blocked: string;
}

/**
 * The starts a run offers, and the one chosen first. Without a proposal the
 * agents write it from the brief and there is nothing to choose. With one, the
 * first choice keeps it as it is (checks, then reviewers) unless it has no
 * tests yet. Keeping the proposal keeps the creation stage, so the daemon
 * requires its model unchanged since the last run.
 */
export function runStartOptions(session: { changes?: { path: string }[] | null; workflow?: ChangeWorkflow }, createModel: string, isTestPath: (path: string) => boolean): { options: RunStartOption[]; initial: RunStart } {
  const changes = session.changes ?? [];
  if (!changes.length) return { options: [], initial: 'create' };
  const kept = session.workflow?.stages.find(stage => stage.name === 'create')?.model?.profile;
  const blocked = session.workflow && kept !== createModel ? 'The creation model changed since the last run, so the agents write the change again.' : '';
  const pause = session.workflow?.status === 'paused' ? session.workflow.pause : undefined;
  const options: RunStartOption[] = [
    ...(pause?.resume_from === 'review' ? [{ value: 'review' as const, label: 'Continue the review', detail: 'Keeps the change and its passing checks: only the reviewers run.', blocked }] : []),
    { value: 'checks', label: 'Check and review this version', detail: 'Keeps the code and tests as they are: runs the checks, then the reviewers.', blocked },
    { value: 'test', label: 'Write new tests, then check and review', detail: 'Keeps the implementation; the testing agent rewrites the test files.', blocked },
    { value: 'create', label: 'Write the change again', detail: 'The creation agent revises the code from the brief, the conversation and this version, then tests, checks and review follow.', blocked: '' },
  ];
  // A paused run continues where it stopped.
  const resumed = options.find(option => option.value === pause?.resume_from && !option.blocked);
  const initial: RunStart = resumed ? resumed.value : blocked ? 'create' : changes.some(change => isTestPath(change.path)) ? 'checks' : 'test';
  return { options, initial };
}

/** The title of a paused run: when the plan's limit resets, when the CLI said. */
export function pauseTitle(pause: WorkflowPause | undefined, now = new Date()): string {
  const plan = { claude: 'Claude plan', agy: 'Google AI plan', pi: 'Pi subscription' }[pause?.provider ?? ''] ?? 'plan';
  const resets = pause?.resumes_at ? new Date(pause.resumes_at) : undefined;
  if (!resets || Number.isNaN(resets.getTime())) return `Paused: the ${plan} reached its usage limit`;
  const time = resets.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  const sameDay = resets.toDateString() === now.toDateString();
  const when = sameDay ? `at ${time}` : `on ${resets.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} at ${time}`;
  return resets <= now ? `Paused: the ${plan}'s limit has reset, resume the run` : `Paused until the ${plan}'s limit resets ${when}`;
}

/** Why a new task cannot get its own worktree from the main checkout, or ''. */
export function ownWorktreeBlocker(git: GitWorkspace | undefined, trusted: boolean): string {
  if (!git?.available) return 'Own worktrees need a Git repository.';
  if (!git.branch || !git.head) return 'Own worktrees need a branch with a commit.';
  if (!trusted) return 'Creating a worktree needs trust (Settings → Trust), since Git can run repository filters.';
  return '';
}

/** The tasks saved on a worktree, once each: its history includes the integrated ones. */
export function worktreeTasks(tree: GitWorktree): ChangeHistoryEntry[] {
  const seen = new Set<string>();
  return [...(tree.sessions ?? []), ...(tree.completed ?? [])].filter(task => !seen.has(task.id) && Boolean(seen.add(task.id)));
}

function integrated(task: ChangeHistoryEntry): boolean {
  return task.state === 'applied' && task.commit?.state === 'committed';
}

/**
 * Whether a worktree can be removed without losing work: a clean, unlocked
 * branch checkout whose commits the main branch already has (integration
 * `merged`, which includes a branch with no commits of its own) and whose
 * tasks are all applied and committed.
 */
export function worktreeRemovable(tree: GitWorktree): boolean {
  if (tree.current || tree.locked || tree.prunable || tree.dirty || tree.error || !tree.branch) return false;
  return tree.integration === 'merged' && worktreeTasks(tree).every(integrated);
}

/**
 * Whether a worktree left with no tasks still holds work of its own (changes
 * not committed, or commits the main branch lacks): it can only be discarded
 * with that work, or merged with Git.
 */
export function worktreeDiscardable(tree: GitWorktree): boolean {
  if (tree.current || tree.locked || tree.prunable || tree.error || !tree.branch) return false;
  return !worktreeTasks(tree).length && !worktreeRemovable(tree);
}

/** The guarded merge (or preview) request for a worktree into the main checkout. */
export function mergeRequest(workspace: GitWorkspace, worktree: GitWorktree, project: ProjectIdentity) {
  return {
    project_id: project.project_id,
    project_revision: project.project_revision,
    head: workspace.head,
    branch: workspace.branch,
    path: worktree.path,
    source_head: worktree.head,
    source_branch: worktree.branch,
    confirm: true,
    confirm_execution: true,
  };
}

export function parseCommits(text: string | undefined): { sha: string; subject: string }[] {
  return (text ?? '')
    .split('\n')
    .map(line => line.trim())
    .filter(Boolean)
    .map(line => {
      const space = line.indexOf(' ');
      return space > 0 ? { sha: line.slice(0, space), subject: line.slice(space + 1) } : { sha: line, subject: '' };
    });
}

/**
 * The task fixing an issue: the newest live task that links it, a draft or a
 * task applied in its worktree and not merged yet. `tasks` is newest first.
 */
export function taskFixingIssue<T extends { state: string; worktreePath: string; finding_targets?: { id: string }[] }>(tasks: T[], findingId: string): T | undefined {
  return tasks.find(task => (task.state === 'draft' || (task.state === 'applied' && Boolean(task.worktreePath))) && Boolean(task.finding_targets?.some(target => target.id === findingId)));
}

/**
 * Explains why the composer cannot start agents on a new task, or '': the same
 * rules the daemon applies when the run starts.
 */
export function composerStartBlocker(paths: string[], trusted: boolean): string {
  if (!paths.length) return 'Choose at least one file the agents may change.';
  if (!trusted) return 'Allow this project to run checks (Settings → Trust) so agents can verify their work.';
  return '';
}

/** Explains why the agent workflow cannot start for this task, or ''. */
export function workflowBlocker(session: { state: string; freshness: string; kind: string; workflow?: ChangeWorkflow; worktree?: unknown } | null, trusted: boolean): string {
  if (!session) return 'Select a task first.';
  if (session.state === 'applied') return 'This task is already accepted.';
  if (session.freshness && session.freshness !== 'current') return 'Its files changed since this task was captured. Re-analyze it first.';
  if (session.kind === 'instructions') return 'Instruction edits use the manual proposal flow.';
  if (session.workflow && ['running', 'canceling'].includes(session.workflow.status)) return 'Agents are already working on this task.';
  if (!session.worktree) return 'This task was made in the main checkout; Kasuri no longer runs agents on it.';
  if (!trusted) return 'Allow this project to run checks (Settings → Trust) so agents can verify their work.';
  return '';
}

/** How an issue's step reads on the task page. */
export function issueStepState(status: string | undefined): { label: string; tone: PhaseTone; badge: Tone } {
  switch (status) {
    case 'fixing':
      return { label: 'Fixing', tone: 'running', badge: 'info' };
    case 'fixed':
      return { label: 'Fixed', tone: 'passed', badge: 'success' };
    case 'not_fixed':
      return { label: 'Not fixed', tone: 'failed', badge: 'warning' };
    default:
      return { label: 'Waiting', tone: 'idle', badge: 'neutral' };
  }
}
