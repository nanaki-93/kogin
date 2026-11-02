// Review authority rules mirrored from the daemon. The daemon remains the
// authority: these helpers only decide which controls are offered, so a stale
// or incomplete proposal never presents Accept as available.

import type { ChangeComment, ChangeHistoryEntry, ChangeSession, Check, FileChange, LoadedSession } from '@/api/types';
import type { Tone } from './util';

/** waiting: nothing runs until someone acts (your review, or a plan's reset). */
export type PhaseTone = 'passed' | 'failed' | 'running' | 'waiting' | 'idle';

/** Normalizes check, stage and verdict states into four display tones. */
export function phaseTone(raw: string | undefined): PhaseTone {
  const s = (raw ?? '').toLowerCase().trim();
  if (['passed', 'completed', 'completed_empty', 'fresh', 'approve', 'approved', 'checked', 'reviewed', 'applied', 'committed', 'merged', 'verified'].includes(s)) return 'passed';
  if (['failed', 'error', 'rejected', 'changes_requested', 'stale', 'outdated', 'interrupted', 'conflicts', 'blocked'].includes(s)) return 'failed';
  if (['waiting', 'awaiting_human_review', 'paused'].includes(s)) return 'waiting';
  if (['running', 'in_progress', 'working', 'queued', 'canceling', 'pausing', 'pending_review'].includes(s)) return 'running';
  return 'idle';
}

export function isFileReviewed(session: ChangeSession, path: string): boolean {
  return Boolean(session.hash) && session.file_review_hash === session.hash && (session.reviewed_files ?? []).includes(path);
}

/** Edits shown for review: the proposal plus pinned tests, or the delta since the last review. */
export function effectiveEdits(session: ChangeSession | null | undefined, sinceLastReview = false): FileChange[] {
  if (!session) return [];
  if (sinceLastReview) return session.review_diff ?? [];
  const seen = new Set<string>();
  return [...(session.changes ?? []), ...(session.pinned_tests ?? [])].filter(edit => {
    if (!edit?.path || seen.has(edit.path)) return false;
    seen.add(edit.path);
    return true;
  });
}

export function reviewProgress(session: ChangeSession) {
  const edits = session.changes ?? [];
  const reviewed = edits.filter(edit => isFileReviewed(session, edit.path)).length;
  const total = edits.length;
  return { reviewed, total, percent: total ? Math.round((reviewed / total) * 100) : 0 };
}

export interface CheckSummary {
  total: number;
  passed: number;
  failed: number;
  running: number;
  idle: number;
  label: string;
  tone: Tone;
}

/**
 * Summarizes checks; only a failed required check blocks Apply, so optional
 * failures read as a warning. Outdated checks ran on an earlier project tree.
 */
export function checkSummary(checks: Check[] | undefined, outdated = false): CheckSummary {
  const list = checks ?? [];
  const counts = { total: list.length, passed: 0, failed: 0, running: 0, idle: 0 };
  for (const check of list) {
    const tone = phaseTone(check.state);
    counts[tone === 'waiting' ? 'running' : tone]++;
  }
  const requiredFailed = list.filter(check => check.required && phaseTone(check.state) === 'failed').length;
  const optionalFailed = counts.failed - requiredFailed;
  let label: string;
  let tone: Tone;
  if (!list.length) [label, tone] = ['Not run', 'neutral'];
  else if (outdated) [label, tone] = ['Outdated', 'warning'];
  else if (requiredFailed) [label, tone] = [`${requiredFailed} required failed`, 'danger'];
  else if (counts.running) [label, tone] = ['Running', 'info'];
  else if (optionalFailed) [label, tone] = [`${optionalFailed} optional failed`, 'warning'];
  else if (counts.passed === list.length) [label, tone] = [`${counts.passed}/${list.length} passed`, 'success'];
  else [label, tone] = [`${counts.passed}/${list.length} passed`, 'warning'];
  return { ...counts, label, tone };
}

export function workflowActive(session: { workflow?: { status: string } } | null | undefined): boolean {
  const status = session?.workflow?.status;
  return status === 'running' || status === 'canceling';
}

/** Where a task made before every task got its own worktree lives. */
export const CHECKOUT_TASK = 'Made in the main checkout, before every task got its own worktree.';

export type ReviewStep = 'none' | 'generate' | 'agents' | 'review' | 'checks' | 'approve' | 'apply' | 'applied' | 'stale' | 'checkout';

export interface Readiness {
  editable: boolean;
  filesReviewed: boolean;
  checksPassed: boolean;
  canApprove: boolean;
  canApply: boolean;
  step: ReviewStep;
  message: string;
}

/**
 * Accept needs every changed file reviewed at the current hash, all required
 * checks passed, a workflow (if any) waiting for human review, and an explicit
 * approval recorded for this exact hash. A task made in the main checkout,
 * before every task got its own worktree, can only be read and deleted.
 */
export function proposalReadiness(session: ChangeSession | null | undefined): Readiness {
  const result: Readiness = { editable: false, filesReviewed: false, checksPassed: false, canApprove: false, canApply: false, step: 'none', message: 'Select a task.' };
  if (!session) return result;
  if (session.state === 'applied') {
    const message = session.worktree ? `Accepted as a commit on ${session.worktree.branch}. Merge it from Branches, or remove the worktree to drop it.` : 'Applied in the main checkout, before every task got its own worktree.';
    return { ...result, step: 'applied', message };
  }
  if (!session.worktree) return { ...result, step: 'checkout', message: CHECKOUT_TASK };
  if (session.freshness !== 'current') return { ...result, step: 'stale', message: `${session.stale_reason ? `Out of date: ${session.stale_reason}.` : 'The project changed since this proposal was made.'} Re-analyze it against the current files, or delete it.` };
  result.editable = true;
  const edits = session.changes ?? [];
  result.filesReviewed = edits.length > 0 && edits.every(edit => isFileReviewed(session, edit.path));
  const checks = session.checks ?? [];
  result.checksPassed = checks.length > 0 && !session.checks_outdated && checks.every(check => !check.required || check.state === 'passed');
  const workflowReady = !session.workflow || session.workflow.status === 'awaiting_human_review';
  result.canApprove = result.filesReviewed && result.checksPassed && workflowReady;
  result.canApply = result.canApprove && Boolean(session.hash) && session.reviewed_hash === session.hash;
  if (!edits.length) {
    result.step = workflowActive(session) ? 'agents' : 'generate';
    result.message = workflowActive(session) ? 'Agents are writing the change.' : 'No changes yet. Start the agents to generate them.';
  } else if (workflowActive(session)) {
    result.step = 'agents';
    result.message = 'Agents are still working on this version.';
  } else if (!result.filesReviewed) {
    result.step = 'review';
    result.message = 'Review each changed file and mark it viewed.';
  } else if (!result.checksPassed) {
    result.step = 'checks';
    result.message = session.checks_outdated ? 'The project changed since checks ran. Run them again before approval.' : 'Run checks; required checks must pass before approval.';
  } else if (!workflowReady) {
    result.step = 'agents';
    const rejectedByModel = session.workflow?.status === 'changes_requested' && !session.workflow.input;
    result.message = session.workflow?.reason || (rejectedByModel ? 'The reviewers requested changes. Run the agents again, or override the reviewers with a reason.' : 'The agent workflow has not reached human review.');
  } else if (!result.canApply) {
    result.step = 'approve';
    result.message = 'Approve this exact version to enable Accept.';
  } else {
    result.step = 'apply';
    result.message = `Approved. Accept commits this version on ${session.worktree.branch}.`;
  }
  return result;
}

/**
 * The human may take the last word when the model reviewers rejected the
 * current version: a finished run, the verdict at this hash, passing checks
 * and no human decision yet. The daemon re-checks every condition.
 */
export function canOverrideReview(session: ChangeSession | null | undefined): boolean {
  const workflow = session?.workflow;
  const ready = proposalReadiness(session);
  return Boolean(workflow) && ready.editable && ready.checksPassed && workflow!.status === 'changes_requested' && !workflow!.input && workflow!.review?.verdict === 'changes_requested' && workflow!.review.proposal_hash === session!.hash;
}

/** Comments not yet sent to the agents, which join the next revision or run. */
export function openComments(session: Pick<ChangeSession, 'comments'> | null | undefined): ChangeComment[] {
  return (session?.comments ?? []).filter(comment => !comment.sent_revision);
}

/** The next unreviewed edit after `current`, wrapping around, or ''. */
export function nextUnreviewedPath(session: ChangeSession, edits: FileChange[], current: string): string {
  if (!edits.length) return '';
  const start = edits.findIndex(edit => edit.path === current);
  for (let offset = 1; offset <= edits.length; offset++) {
    const candidate = edits[(start + offset + edits.length) % edits.length];
    if (candidate && !isFileReviewed(session, candidate.path)) return candidate.path;
  }
  return '';
}

/** The guarded proposal identity every mutation request carries. */
export function changeIdentity(session: LoadedSession) {
  return {
    project_id: session.checkout.project_id || session.project_id,
    project_revision: session.checkout.project_revision || session.project_revision,
    revision: session.revision,
    hash: session.hash,
  };
}

export type TaskStatus = 'draft' | 'running' | 'needs_you' | 'ready' | 'applied' | 'failed' | 'stale' | 'canceled' | 'done';

export interface TaskStatusInfo {
  status: TaskStatus;
  label: string;
  tone: Tone;
}

/** A task's status for lists, from its history entry alone. */
export function taskStatus(entry: Pick<ChangeHistoryEntry, 'state' | 'freshness' | 'workflow_status' | 'commit' | 'revision' | 'hash'>): TaskStatusInfo {
  const wf = entry.workflow_status ?? '';
  if (entry.state === 'applied') return { status: 'applied', label: entry.commit?.state === 'committed' ? 'Committed' : 'Applied', tone: 'success' };
  if (entry.state === 'undone') return { status: 'done', label: 'Undone', tone: 'neutral' };
  if (wf === 'running' || wf === 'canceling' || wf === 'queued') return { status: 'running', label: wf === 'canceling' ? 'Canceling' : 'Agents working', tone: 'info' };
  // Another task changes the same code: this one continues on its own after it.
  if (wf === 'waiting') return { status: 'running', label: 'Waiting', tone: 'neutral' };
  if (entry.freshness && entry.freshness !== 'current') return { status: 'stale', label: 'Stale', tone: 'warning' };
  if (wf === 'awaiting_human_review') return { status: 'needs_you', label: 'Needs review', tone: 'accent' };
  if (wf === 'changes_requested') return { status: 'needs_you', label: 'Changes requested', tone: 'warning' };
  // A plan reached its usage limit: the run continues when you resume it.
  if (wf === 'paused') return { status: 'needs_you', label: 'Paused', tone: 'warning' };
  // No issue needed a change the agents could make: close the task.
  if (wf === 'nothing_to_fix') return { status: 'needs_you', label: 'Nothing to fix', tone: 'neutral' };
  // The proposal changed after the last run: the agents run again, not failed.
  if (wf === 'outdated') return { status: 'needs_you', label: 'Revised', tone: 'accent' };
  if (wf === 'failed' || wf === 'stale' || wf === 'interrupted') return { status: 'failed', label: wf === 'interrupted' ? 'Interrupted' : 'Failed', tone: 'danger' };
  if (wf === 'canceled') return { status: 'canceled', label: 'Canceled', tone: 'neutral' };
  if (entry.revision > 0 && entry.hash) return { status: 'needs_you', label: 'Needs review', tone: 'accent' };
  return { status: 'draft', label: 'Draft', tone: 'neutral' };
}
