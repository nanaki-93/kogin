// Review actions: line comments, viewed files, checks, approval, reviewer
// override and rejection, then Accept: a commit on the task's own branch.

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

  /** Approves this exact version; the daemon also answers a waiting workflow input. */
  async function approve(session: LoadedSession, notes = ''): Promise<boolean> {
    if (!proposalReadiness(session).canApprove) return false;
    const result = await guarded('Approving…', async () => {
      try {
        const body = notes.trim() ? { ...changeIdentity(session), notes: notes.trim() } : changeIdentity(session);
        setSession(await api.post<ChangeSession>(changePath(session, '/review'), body), session);
        toast('Approved this exact version. You can accept it now.', 'success');
        return true;
      } catch (err) {
        fail('Approval failed', err);
        return false;
      }
    });
    return Boolean(result);
  }

  /** Overrides the reviewers' rejection of this version; it approves nothing. */
  async function overrideReview(session: LoadedSession, reason: string): Promise<boolean> {
    if (!canOverrideReview(session) || !reason.trim()) return false;
    const result = await guarded('Overriding…', async () => {
      try {
        setSession(await api.post<ChangeSession>(changePath(session, '/review-override'), { ...changeIdentity(session), workflow_id: session.workflow!.id, reason: reason.trim() }), session);
        toast('Reviewers overridden. View the files and approve this version to accept it.', 'info');
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

  /** Accepts a task: its reviewed version is committed on the task branch, in its own worktree. */
  async function apply(session: LoadedSession, message: string) {
    if (!proposalReadiness(session).canApply || !session.worktree) return;
    const branch = session.worktree.branch;
    if (!message.trim()) {
      toast('Enter a commit message.', 'warning');
      return;
    }
    const ok = await confirm({
      title: 'Accept and commit?',
      body: `Commits “${session.title || session.id}” on ${branch}, exactly as reviewed. Its files are already in the task's worktree; merge the branch from Branches when you are ready.`,
      confirmLabel: 'Accept & commit',
    });
    if (!ok) return;
    await guarded('Accepting…', async () => {
      try {
        const result = await api.post<ChangeMutationResult>(changePath(session, '/apply'), { ...changeIdentity(session), confirm: true, commit_message: message.trim() });
        toast(`Accepted and committed on ${branch}.`, 'success');
        for (const warning of result?.warnings ?? []) toast(warning, 'warning');
        await refreshAll();
      } catch (err) {
        fail('Accept failed', err);
      }
    });
  }

  return { markViewed, runChecks, addComment, deleteComment, approve, overrideReview, reject, apply };
}
