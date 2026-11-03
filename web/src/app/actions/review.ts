// Review actions: line comments, viewed files, checks, reviewer override and
// rejection, then Accept: your approval of the version and a commit on the
// task's own branch, in one step.

import { api } from '@/api/client';
import type { ChangeMutationResult, ChangeSession, LoadedSession } from '@/api/types';
import { canOverrideReview, changeIdentity, proposalReadiness } from '@/lib/readiness';
import { plural } from '@/lib/util';
import { changePath, type ActionContext } from './context';

export function reviewActions(context: ActionContext) {
  const { confirm, toast, guarded, fail, setSession, refreshAll } = context;

  // ---------------------------------------------------------------- review

  async function addComment(session: LoadedSession, anchor: { path: string; side: 'new' | 'old'; line: number }, text: string): Promise<boolean> {
    return Boolean(
      await guarded('Saving comment…', async () => {
        try {
          setSession(await api.post<ChangeSession>(changePath(session, '/comments'), { ...changeIdentity(session), ...anchor, text: text.trim() }), session);
          return true;
        } catch (err) {
          fail('Could not save the comment', err);
          return false;
        }
      }),
    );
  }

  async function deleteComment(session: LoadedSession, id: string): Promise<void> {
    await guarded('Deleting comment…', async () => {
      try {
        setSession(await api.post<ChangeSession>(changePath(session, '/comments/delete'), { ...changeIdentity(session), id }), session);
      } catch (err) {
        fail('Could not delete the comment', err);
      }
    });
  }

  async function markViewed(session: LoadedSession, path: string): Promise<boolean> {
    return Boolean(
      await guarded('Saving review…', async () => {
        try {
          setSession(await api.post<ChangeSession>(changePath(session, '/review-file'), { ...changeIdentity(session), path }), session);
          return true;
        } catch (err) {
          fail(`Could not mark ${path} viewed`, err);
          return false;
        }
      }),
    );
  }

  async function runChecks(session: LoadedSession) {
    await guarded('Running checks…', async () => {
      try {
        const updated = await api.post<ChangeSession>(changePath(session, '/checks'), { ...changeIdentity(session), run_lint: true, run_tests: true });
        setSession(updated, session);
        const failed = (updated.checks ?? []).filter(check => check.state === 'failed').length;
        toast(failed ? `${plural(failed, 'check')} failed.` : 'All checks passed.', failed ? 'warning' : 'success');
      } catch (err) {
        fail('Checks could not run', err);
      }
    });
  }

  /** Overrides the reviewers' rejection of this version; it approves nothing. */
  async function overrideReview(session: LoadedSession, reason: string): Promise<boolean> {
    if (!canOverrideReview(session) || !reason.trim()) return false;
    const result = await guarded('Overriding…', async () => {
      try {
        setSession(await api.post<ChangeSession>(changePath(session, '/review-override'), { ...changeIdentity(session), workflow_id: session.workflow!.id, reason: reason.trim() }), session);
        toast('Reviewers overridden. View the files, then accept this version.', 'info');
        return true;
      } catch (err) {
        fail('Could not override the reviewers', err);
        return false;
      }
    });
    return Boolean(result);
  }

  async function reject(session: LoadedSession) {
    const input = session.workflow?.input;
    if (!input || input.status !== 'waiting') return;
    if (!(await confirm({ title: 'Reject this proposal?', body: 'The workflow ends and this version can no longer be approved.', confirmLabel: 'Reject', danger: true }))) return;
    await guarded('Rejecting…', async () => {
      try {
        setSession(await api.post<ChangeSession>(changePath(session, '/workflow/input'), { ...changeIdentity(session), workflow_id: session.workflow!.id, request_id: input.request_id, decision: 'reject' }), session);
        toast('Proposal rejected.', 'info');
      } catch (err) {
        fail('Could not reject', err);
      }
    });
  }

  /**
   * Accepts a task in one step: records the approval of this exact version
   * (with optional notes; the daemon also answers a waiting workflow input),
   * unless it is already approved, then commits it on the task branch in its
   * own worktree. The caller confirms first.
   */
  async function accept(session: LoadedSession, message: string, notes = ''): Promise<boolean> {
    const ready = proposalReadiness(session);
    if (!ready.canAccept || !session.worktree) return false;
    const branch = session.worktree.branch;
    const commitMessage = message.trim();
    if (!commitMessage) {
      toast('Enter a commit message.', 'warning');
      return false;
    }
    const result = await guarded('Accepting…', async () => {
      let current = session;
      try {
        if (!ready.approved) {
          const body = notes.trim() ? { ...changeIdentity(session), notes: notes.trim() } : changeIdentity(session);
          const approved = await api.post<ChangeSession>(changePath(session, '/review'), body);
          setSession(approved, session);
          current = { ...approved, checkout: session.checkout };
        }
        const applied = await api.post<ChangeMutationResult>(changePath(current, '/apply'), { ...changeIdentity(current), confirm: true, commit_message: commitMessage });
        toast(`Accepted and committed on ${branch}.`, 'success');
        for (const warning of applied?.warnings ?? []) toast(warning, 'warning');
        await refreshAll();
        return true;
      } catch (err) {
        // An approval that went through stays: Accept then only commits.
        fail(current === session ? 'Accept failed' : 'Approved, but the commit failed', err);
        return false;
      }
    });
    return Boolean(result);
  }

  return { markViewed, runChecks, addComment, deleteComment, overrideReview, reject, accept };
}
