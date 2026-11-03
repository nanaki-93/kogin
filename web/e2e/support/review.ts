// The fake daemon's review transitions of a task, as pure functions of the
// task and the request body: approve, override the reviewers, revise and
// start an agent run, each with the daemon's guards.

import type { ChangeSession } from '../../src/api/types';
import * as data from './fixtures';

/** Approval answers a waiting input and keeps the reviewer's notes with this hash. */
export function approveSession(s: ChangeSession, body: Record<string, unknown>): ChangeSession {
  const workflow = s.workflow;
  if (workflow && workflow.status !== 'awaiting_human_review') throw new Error('The workflow has not reached human review.');
  const notes = typeof body.notes === 'string' ? body.notes : '';
  const input = workflow?.input?.status === 'waiting' ? { ...workflow.input, status: 'approved' } : workflow?.input;
  return { ...s, reviewed_hash: s.hash, review_notes: notes || undefined, workflow: workflow && { ...workflow, input } };
}

/** Only the reviewers' rejection of this exact hash can be overridden, with a reason. */
export function overrideReview(s: ChangeSession, body: Record<string, unknown>): ChangeSession {
  const workflow = s.workflow;
  if (!workflow || body.workflow_id !== workflow.id) throw new Error('The workflow changed since it was loaded.');
  if (workflow.status !== 'changes_requested' || workflow.input || workflow.review?.proposal_hash !== s.hash) throw new Error('Only a proposal the model reviewers rejected can be overridden.');
  if (typeof body.reason !== 'string' || !body.reason.trim()) throw new Error('Give a reason for overriding the reviewers.');
  const stages = workflow.stages.map(stage => (stage.name === 'human_review' ? { ...stage, status: 'waiting' } : stage));
  return { ...s, workflow: { ...workflow, status: 'awaiting_human_review', stages, override: { proposal_hash: s.hash, reason: body.reason.trim(), at: data.ago(0) } } };
}

/** A revision is a new version; the open comments it carried are marked sent. */
export function reviseSession(s: ChangeSession, body: Record<string, unknown>): ChangeSession {
  const open = (s.comments ?? []).filter(comment => !comment.sent_revision);
  if (body.include_comments && !open.length) throw new Error('There are no open line comments to send.');
  const comments = (s.comments ?? []).map(comment => (body.include_comments && !comment.sent_revision ? { ...comment, sent_revision: s.revision } : comment));
  // A new version outdates the last run, as invalidateChangeWorkflow does.
  const workflow = s.workflow && { ...s.workflow, status: 'outdated', reason: 'The proposal changed. Run the workflow again before human review.', stages: s.workflow.stages.map(stage => (stage.name === 'human_review' ? { ...stage, status: 'blocked' } : stage)) };
  return { ...s, revision: s.revision + 1, hash: `${s.hash}-r${s.revision + 1}`, comments, reviewed_files: [], reviewed_hash: undefined, checks: [], workflow, messages: [...s.messages, { role: 'user', content: String(body.message || 'Line comments') }, { role: 'assistant', content: 'Addressed the comments.' }] };
}

/**
 * Admits a run like the daemon: a start that keeps the proposal keeps the
 * creation stage, whose model must not have changed; the run takes a new revision.
 */
export function startRun(s: ChangeSession, body: Record<string, unknown>): ChangeSession {
  if (s.workflow && ['running', 'canceling'].includes(s.workflow.status)) throw new Error('a change workflow is already active');
  // Like the daemon: no run builds on files the main branch has since changed.
  const main = s.main_changed;
  if (main) throw new Error(`${main.branch} changed ${main.paths.join(', ')} since this task started. This task's work is built on ${main.paths.length === 1 ? 'that file as it was' : 'those files as they were'}: re-analyze it against ${main.branch} and, if the issue is still there, start it again from ${main.branch}.`);
  const start = String(body.resume_from ?? 'create');
  const kept = { create: 0, test: 1, checks: 1, review: 2 }[start];
  if (kept === undefined) throw new Error('resume_from must be create, test, checks or review');
  if (kept && !s.changes.length) throw new Error(`cannot resume ${start} without current proposal evidence; run the preceding stage`);
  const models = body.models as { create: string; test: string; review: string };
  const previous = s.workflow?.stages.find(stage => stage.name === 'create');
  if (kept && s.workflow && previous?.model?.profile !== models.create) throw new Error('retry from create to change its model, or retain its previously selected model');
  const selections = [models.create, models.test, models.review];
  const stages = ['create', 'test', 'review'].map((name, i) => (i < kept ? { ...s.workflow!.stages[i], status: 'completed' } : { name, status: 'pending', model: data.stageModel(selections[i]) }));
  const revision = s.revision + 1;
  const workflow = { id: `${s.id}-${revision}`, status: 'running', resume_from: body.resume_from as string | undefined, attempt: (s.workflow?.attempt ?? 1) + 1, auto_repair: Boolean(body.auto_repair), stages: [...stages, { name: 'human_review', status: 'blocked' }] };
  return { ...s, revision, reviewed_hash: undefined, checks: start === 'review' ? s.checks : [], workflow };
}
