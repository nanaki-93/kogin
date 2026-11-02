import { describe, expect, it } from 'vitest';
import type { ChangeSession, Check, FileChange } from '@/api/types';
import { canOverrideReview, checkSummary, effectiveEdits, nextUnreviewedPath, phaseTone, proposalReadiness, reviewProgress, taskStatus } from './readiness';

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
  it('walks review → checks → approve → apply', () => {
    expect(proposalReadiness(session()).step).toBe('review');
    expect(proposalReadiness(session({ ...reviewed })).step).toBe('checks');
    const checked = session({ ...reviewed, checks: [check('lint', 'passed'), check('tests', 'passed'), check('optional', 'failed', false)] });
    const approve = proposalReadiness(checked);
    expect(approve).toMatchObject({ step: 'approve', canApprove: true, canApply: false });
    expect(proposalReadiness({ ...checked, reviewed_hash: 'sha-2' })).toMatchObject({ step: 'apply', canApply: true });
  });

  it('does not count reviews recorded for an older hash', () => {
    const r = proposalReadiness(session({ file_review_hash: 'sha-1', reviewed_files: ['a.go', 'a_test.go'], checks: [check('tests', 'passed')], reviewed_hash: 'sha-1' }));
    expect(r.filesReviewed).toBe(false);
    expect(r.canApply).toBe(false);
  });

  it('waits for a workflow to reach human review', () => {
    const base = session({ ...reviewed, checks: [check('tests', 'passed')], reviewed_hash: 'sha-2' });
    expect(proposalReadiness({ ...base, workflow: { status: 'running' } as ChangeSession['workflow'] }).step).toBe('agents');
    expect(proposalReadiness({ ...base, workflow: { status: 'changes_requested', reason: 'Reviewer asked for tests' } as ChangeSession['workflow'] })).toMatchObject({ step: 'agents', canApply: false, message: 'Reviewer asked for tests' });
    expect(proposalReadiness({ ...base, workflow: { status: 'awaiting_human_review' } as ChangeSession['workflow'] }).canApply).toBe(true);
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
    expect(proposalReadiness(outdated)).toMatchObject({ step: 'checks', checksPassed: false, canApprove: false });
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
    expect(proposalReadiness(checkout)).toMatchObject({ step: 'checkout', editable: false, canApprove: false, canApply: false });
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
    expect(taskStatus({ ...entry, revision: 2, hash: 'x', workflow_status: 'paused' })).toEqual({ status: 'needs_you', label: 'Paused', tone: 'warning' });
    // A revision after the run needs the agents again; it is not a failure.
    expect(taskStatus({ ...entry, revision: 4, hash: 'x', workflow_status: 'outdated' })).toEqual({ status: 'needs_you', label: 'Revised', tone: 'accent' });
    expect(taskStatus({ ...entry, state: 'applied', commit: { state: 'committed' } as never }).label).toBe('Committed');
  });
});
