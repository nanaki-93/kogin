// Housekeeping: upkeep of the tests, as against work on the code that runs.
// An open issue in a test file is housekeeping, and so is the task that fixes
// only such issues in only test files. A reviewed housekeeping task that
// changed nothing but tests, passed its checks, was approved by the reviewers
// and stayed within what it was asked can be accepted from the Inbox in one
// step with the others, without opening each.

import type { DiffLine, LoadedSession } from '@/api/types';
import { planResult, reviewHunks } from './hunks';
import { behaviorChanges, proposalReadiness } from './readiness';
import { basename, isTestPath, plural } from './util';
import { reviewVerdict } from './workflow';

/** A task that fixes issues of the analysis and touches only test files. */
export function isHousekeepingTask(task: { finding_targets?: unknown[]; paths?: string[] }): boolean {
  const paths = task.paths ?? [];
  return (task.finding_targets ?? []).length > 0 && paths.length > 0 && paths.every(isTestPath);
}

/**
 * Why a reviewed task still needs you to open it; none when it can be
 * accepted as it is. Each reason is one the review would have stopped at:
 * code that runs changed, a check or the reviewers did not pass it, its
 * behavior changed, or a change is outside its plan or unexplained.
 */
export function acceptBlockers(session: LoadedSession): string[] {
  const ready = proposalReadiness(session);
  const edits = session.changes ?? [];
  if (!ready.editable || session.workflow?.status !== 'awaiting_human_review' || !edits.length) return ['it is not waiting for review'];
  const blockers: string[] = [];
  const tests = new Set((session.targets ?? []).filter(target => target.test).map(target => target.path));
  const code = edits.filter(edit => !tests.has(edit.path) && !isTestPath(edit.path));
  if (code.length) blockers.push(`it changes ${basename(code[0].path)}${code.length > 1 ? ` and ${plural(code.length - 1, 'other file')}` : ''}, not only tests`);
  if (!ready.checksPassed) blockers.push('its checks have not all passed');
  if (reviewVerdict(session.workflow, session.hash) !== 'approve') blockers.push('the reviewers did not approve it');
  const behavior = behaviorChanges(session).length;
  if (behavior) blockers.push(`${plural(behavior, 'behavior change')} to accept`);
  const linesOf = new Map<string, DiffLine[]>(edits.map(edit => [edit.path, edit.diff?.lines ?? []]));
  const result = planResult(session, linesOf, reviewHunks(edits));
  const outside = result.blocks.filter(block => block.exception === 'outside').length;
  // Without a plan to set it against, every hunk must at least be explained.
  const explained = session.walkthrough?.status === 'ready' && session.walkthrough.proposal_hash === session.hash;
  const bare = result.blocks.filter(block => !block.notes.length).length;
  if (outside) blockers.push(`${plural(outside, 'change')} outside its plan`);
  if (!explained) blockers.push('its change is not explained');
  else if (bare) blockers.push(`${plural(bare, 'change')} nobody explained`);
  return blockers;
}

/** The task in one line: what was wrong, in the plan's plain words, else what its change fixes. */
export function digestLine(session: Pick<LoadedSession, 'plan' | 'walkthrough' | 'title' | 'hash'>): string {
  const plan = session.plan?.source === 'investigator' ? session.plan : undefined;
  const summary = session.walkthrough?.proposal_hash === session.hash ? session.walkthrough.files?.find(file => file.summary)?.summary : undefined;
  return plan?.claim || summary || plan?.problem || session.title;
}
