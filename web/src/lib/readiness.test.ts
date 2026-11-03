import { describe, expect, it } from 'vitest';
import type { ChangeSession, Check, FileChange } from '@/api/types';
import { canOverrideReview, checkSummary, effectiveEdits, nextUnreviewedPath, phaseTone, proposalReadiness, reviewProgress, sessionStatusEntry, taskStatus } from './readiness';

function edit(path: string): FileChange {
  return { path, content: '', hash: 'h-' + path, diff: { lines: [] } } as unknown as FileChange;
}

function check(name: string, state: string, required = true): Check {
  return { name, state, required } as Check;
}

function session(overrides: Partial<ChangeSession> = {}): ChangeSession {
  return {
    project_id: 'p', project_revision: 'r', id: 'chg-1', kind: 'fix', title: 'Fix', acceptance_criteria: [],
    revision: 2, hash: 'sha-2', state: 'draft', freshness: 'current', targets: [], changes: [edit('a.go'), edit('a_test.go')],
    messages: [], checks: [], updated_at: '2026-10-01T00:00:00Z', worktree: { base: 'b0', branch: 'orca/fix-a1' }, ...overrides,
  };
}

const reviewed = { file_review_hash: 'sha-2', reviewed_files: ['a.go', 'a_test.go'] };

describe('proposalReadiness', () => {
  it('walks review → checks → accept, approving in the same step', () => {
    expect(proposalReadiness(session()).step).toBe('review');
    expect(proposalReadiness(session({ ...reviewed })).step).toBe('checks');
    const checked = session({ ...reviewed, checks: [check('lint', 'passed'), check('tests', 'passed'), check('optional', 'failed', false)] });
    expect(proposalReadiness(checked)).toMatchObject({ step: 'accept', canAccept: true, approved: false, message: 'Ready. Accept & commit approves this exact version and commits it on orca/fix-a1.' });
    // A version approved before Accept became one step only needs the commit.
    expect(proposalReadiness({ ...checked, reviewed_hash: 'sha-2' })).toMatchObject({ step: 'accept', canAccept: true, approved: true, message: 'Approved. Accept commits this version on orca/fix-a1.' });
  });

  it('does not count reviews recorded for an older hash', () => {
    const r = proposalReadiness(session({ file_review_hash: 'sha-1', reviewed_files: ['a.go', 'a_test.go'], checks: [check('tests', 'passed')], reviewed_hash: 'sha-1' }));
    expect(r).toMatchObject({ filesReviewed: false, canAccept: false, approved: false });
  });

  it('waits for a workflow to reach human review', () => {
    const base = session({ ...reviewed, checks: [check('tests', 'passed')], reviewed_hash: 'sha-2' });
    expect(proposalReadiness({ ...base, workflow: { status: 'running' } as ChangeSession['workflow'] }).step).toBe('agents');
    expect(proposalReadiness({ ...base, workflow: { status: 'changes_requested', reason: 'Reviewer asked for tests' } as ChangeSession['workflow'] })).toMatchObject({ step: 'agents', canAccept: false, message: 'Reviewer asked for tests' });
    expect(proposalReadiness({ ...base, workflow: { status: 'awaiting_human_review' } as ChangeSession['workflow'] }).canAccept).toBe(true);
  });

  it('offers an override only for the reviewers\' rejection of this exact version', () => {
    const review = { proposal_hash: 'sha-2', verdict: 'changes_requested', summary: 'No', findings: [{ title: 'Fix it.' }] };
    const rejected = session({ ...reviewed, checks: [check('tests', 'passed')], workflow: { id: 'w', status: 'changes_requested', stages: [], review } });
    expect(canOverrideReview(rejected)).toBe(true);
    expect(canOverrideReview({ ...rejected, hash: 'sha-3' })).toBe(false);
    expect(canOverrideReview({ ...rejected, checks: [check('tests', 'failed')] })).toBe(false);
    expect(canOverrideReview({ ...rejected, freshness: 'stale' })).toBe(false);
    expect(canOverrideReview({ ...rejected, workflow: { ...rejected.workflow!, input: { request_id: 'r', workflow_id: 'w', status: 'rejected', action: 'review_proposal', expires_at: '' } } })).toBe(false);
    expect(canOverrideReview({ ...rejected, workflow: { ...rejected.workflow!, status: 'awaiting_human_review' } })).toBe(false);
    expect(proposalReadiness(rejected).message).toMatch(/override the reviewers/);
  });

  it('names the changed file of a stale proposal and outdates checks from an earlier tree', () => {
    expect(proposalReadiness(session({ freshness: 'stale', stale_reason: 'go.mod changed since this proposal was made' })).message).toBe('Out of date: go.mod changed since this proposal was made. Re-analyze it against the current files, or delete it.');
    const outdated = session({ ...reviewed, checks: [check('tests', 'passed')], checks_outdated: true });
    expect(proposalReadiness(outdated)).toMatchObject({ step: 'checks', checksPassed: false, canAccept: false });
    expect(proposalReadiness(outdated).message).toMatch(/changed since checks ran/);
    expect(checkSummary(outdated.checks, true)).toMatchObject({ label: 'Outdated', tone: 'warning' });
  });

  it('blocks stale proposals and reports accepted ones', () => {
    expect(proposalReadiness(session({ freshness: 'stale' }))).toMatchObject({ step: 'stale', editable: false });
    expect(proposalReadiness(session({ state: 'applied' }))).toMatchObject({ step: 'applied', message: 'Accepted as a commit on orca/fix-a1. Merge it from Branches, or remove the worktree to drop it.' });
    expect(proposalReadiness(session({ changes: [] })).step).toBe('generate');
  });

  it('keeps a task made in the main checkout to reading and deleting', () => {
    const checkout = session({ ...reviewed, worktree: undefined, checks: [check('tests', 'passed')], reviewed_hash: 'sha-2' });
    expect(proposalReadiness(checkout)).toMatchObject({ step: 'checkout', editable: false, canAccept: false });
    expect(proposalReadiness({ ...checkout, state: 'applied' })).toMatchObject({ step: 'applied', message: 'Applied in the main checkout, before every task got its own worktree.' });
  });
});

describe('review helpers', () => {
  it('counts progress and finds the next unreviewed file with wraparound', () => {
    const s = session({ file_review_hash: 'sha-2', reviewed_files: ['a.go'] });
    expect(reviewProgress(s)).toEqual({ reviewed: 1, total: 2, percent: 50 });
    expect(nextUnreviewedPath(s, s.changes, 'a_test.go')).toBe('a_test.go');
    expect(nextUnreviewedPath(s, s.changes, 'a.go')).toBe('a_test.go');
    expect(nextUnreviewedPath(session({ ...reviewed }), session().changes, 'a.go')).toBe('');
  });

  it('merges pinned tests without duplicates and supports the since-review delta', () => {
    const s = session({ pinned_tests: [edit('a_test.go'), edit('b_test.go')], review_diff: [edit('a.go')] });
    expect(effectiveEdits(s).map(e => e.path)).toEqual(['a.go', 'a_test.go', 'b_test.go']);
    expect(effectiveEdits(s, true).map(e => e.path)).toEqual(['a.go']);
    expect(effectiveEdits(null)).toEqual([]);
  });

  it('summarizes checks', () => {
    expect(checkSummary([])).toMatchObject({ label: 'Not run', tone: 'neutral' });
    expect(checkSummary([check('a', 'passed'), check('b', 'failed')])).toMatchObject({ label: '1 required failed', tone: 'danger' });
    expect(checkSummary([check('a', 'passed'), { ...check('lint', 'failed'), required: false }])).toMatchObject({ label: '1 optional failed', tone: 'warning' });
    expect(checkSummary([check('a', 'passed'), check('b', 'running')])).toMatchObject({ label: 'Running', tone: 'info' });
    expect(checkSummary([check('a', 'passed')])).toMatchObject({ label: '1/1 passed', tone: 'success' });
    expect(phaseTone(' Approved ')).toBe('passed');
    expect(phaseTone('mystery')).toBe('idle');
    // Waiting on a person or a plan's reset is not work in progress.
    expect(['waiting', 'awaiting_human_review', 'paused'].map(phaseTone)).toEqual(['waiting', 'waiting', 'waiting']);
    expect(phaseTone('running')).toBe('running');
  });
});

describe('taskStatus', () => {
  const entry = { state: 'draft', freshness: 'current', revision: 0, hash: '' };
  it('maps history entries to list statuses', () => {
    expect(taskStatus(entry).status).toBe('draft');
    expect(taskStatus({ ...entry, revision: 1, hash: 'x' }).label).toBe('Needs review');
    expect(taskStatus({ ...entry, workflow_status: 'running' }).status).toBe('running');
    expect(taskStatus({ ...entry, workflow_status: 'running', freshness: 'stale' }).status).toBe('running');
    expect(taskStatus({ ...entry, freshness: 'stale' }).status).toBe('stale');
    expect(taskStatus({ ...entry, workflow_status: 'interrupted' }).label).toBe('Interrupted');
    // A run stopped because its files changed, such as on the main branch, is out of date, not failed.
    expect(taskStatus({ ...entry, revision: 2, hash: 'x', workflow_status: 'stale' })).toEqual({ status: 'stale', label: 'Out of date', tone: 'warning' });
    expect(taskStatus({ ...entry, revision: 2, hash: 'x', workflow_status: 'paused' })).toEqual({ status: 'needs_you', label: 'Paused', tone: 'warning' });
    // A revision after the run needs the agents again; it is not a failure.
    expect(taskStatus({ ...entry, revision: 4, hash: 'x', workflow_status: 'outdated' })).toEqual({ status: 'needs_you', label: 'Revised', tone: 'accent' });
    expect(taskStatus({ ...entry, state: 'applied', commit: { state: 'committed' } as never }).label).toBe('Committed');
  });

  it('names what running agents do', () => {
    const running = { ...entry, workflow_status: 'running' };
    expect(taskStatus(running).label).toBe('Agents working');
    expect(taskStatus({ ...running, workflow_phase: { stage: 'create', issue: 2, issues: 5 } })).toEqual({ status: 'running', label: 'Fixing 2 of 5', tone: 'info' });
    expect(taskStatus({ ...running, workflow_phase: { stage: 'create' } }).label).toBe('Writing code');
    expect(taskStatus({ ...running, workflow_phase: { stage: 'create', repair: 1 } }).label).toBe('Repairing');
    expect(taskStatus({ ...running, workflow_phase: { stage: 'test' } }).label).toBe('Testing');
    expect(taskStatus({ ...running, workflow_phase: { stage: 'test', checks_only: true } }).label).toBe('Running checks');
    expect(taskStatus({ ...running, workflow_phase: { stage: 'review' } }).label).toBe('Reviewing');
    expect(taskStatus({ ...entry, workflow_status: 'queued' }).label).toBe('Queued');
    expect(taskStatus({ ...entry, workflow_status: 'canceling', workflow_phase: { stage: 'review' } }).label).toBe('Canceling');
  });

  it('tells a version still to review from one ready to accept', () => {
    const waiting = { ...entry, revision: 3, hash: 'x', workflow_status: 'awaiting_human_review' };
    expect(taskStatus({ ...waiting, review: { step: 'view_files', files: 5, viewed: 0 } })).toEqual({ status: 'needs_you', label: 'Needs review', tone: 'accent', detail: undefined });
    expect(taskStatus({ ...waiting, review: { step: 'view_files', files: 5, viewed: 2 } }).detail).toBe('2 of 5 files viewed');
    expect(taskStatus({ ...waiting, review: { step: 'run_checks', files: 5, viewed: 5 } }).label).toBe('Run checks');
    expect(taskStatus({ ...waiting, review: { step: 'accept', files: 5, viewed: 5 } })).toEqual({ status: 'ready', label: 'Ready to accept', tone: 'success' });
    // A manual proposal waits for you the same way.
    expect(taskStatus({ ...entry, revision: 1, hash: 'x', review: { step: 'accept', files: 1, viewed: 1 } }).status).toBe('ready');
    // An entry from a daemon that reports no review progress still needs review.
    expect(taskStatus(waiting).label).toBe('Needs review');
  });
});

describe('sessionStatusEntry', () => {
  const stages = (running: string) => ['create', 'test', 'review', 'human_review'].map(name => ({ name, status: name === running ? 'running' : 'pending' }));

  it('reads a loaded task the way its list entry does', () => {
    const issues = [{ finding_id: 'a', status: 'fixed' }, { finding_id: 'b', status: 'fixing' }, { finding_id: 'c', status: 'pending' }].map(issue => ({ ...issue, path: 'a.go', title: issue.finding_id, detail: '' }));
    const fixing = session({ changes: [], issues, workflow: { id: 'w', status: 'running', stages: stages('create') } });
    expect(taskStatus(sessionStatusEntry(fixing)).label).toBe('Fixing 2 of 3');
    expect(taskStatus(sessionStatusEntry({ ...fixing, workflow: { id: 'w', status: 'running', resume_from: 'checks', stages: stages('test') } })).label).toBe('Running checks');

    const ready = { id: 'w', status: 'awaiting_human_review', stages: stages('') };
    expect(taskStatus(sessionStatusEntry(session({ workflow: ready, checks: [check('tests', 'passed')], file_review_hash: 'sha-2', reviewed_files: ['a.go'] }))).detail).toBe('1 of 2 files viewed');
    expect(taskStatus(sessionStatusEntry(session({ ...reviewed, workflow: ready, checks: [check('tests', 'passed')] }))).label).toBe('Ready to accept');
    expect(sessionStatusEntry(session({ ...reviewed, workflow: { ...ready, status: 'changes_requested' }, checks: [check('tests', 'passed')] })).review).toBeUndefined();
  });
});
