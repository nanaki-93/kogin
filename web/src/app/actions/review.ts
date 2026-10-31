// Review actions: line comments, viewed files, checks, approval, reviewer
// override and rejection, then Apply (or Accept in a task's own worktree) and
// Undo.

import { api } from '@/api/client';
import type { ChangeMutationResult, ChangeSession, LoadedSession, Project } from '@/api/types';
import { canOverrideReview, changeIdentity, proposalReadiness } from '@/lib/readiness';
import { plural } from '@/lib/util';
import { changePath, type ActionContext } from './context';

export function reviewActions(context: ActionContext) {
  const { confirm, toast, guarded, fail, setSession, refreshAll, runningTasksNote } = context;

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
        toast('Approved this exact version. You can apply it now.', 'success');
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
        toast('Reviewers overridden. View the files and approve this version to apply it.', 'info');
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

  async function apply(session: LoadedSession, project: Project | undefined, options: { commit: boolean; message: string }) {
    if (!proposalReadiness(session).canApply) return;
    if (session.worktree) {
      await accept(session, options.message);
      return;
    }
    const destination = session.checkout.worktreePath || project?.path || 'the current checkout';
    const commit = options.commit && Boolean(session.git?.head);
    if (commit && !options.message.trim()) {
      toast('Enter a commit message or turn off “Commit”.', 'warning');
      return;
    }
    const ok = await confirm({
      title: commit ? 'Apply and commit?' : 'Apply these changes?',
      body: `Writes “${session.title || session.id}” to ${destination}${commit ? ` and commits it on ${session.git?.branch || 'the current branch'}` : ''}. Undo stays available for the latest Apply while the files are unchanged${commit ? '; it restores the files but keeps the commit' : ''}.${runningTasksNote(session, (session.changes ?? []).map(change => change.path))}`,
      confirmLabel: commit ? 'Apply & commit' : 'Apply',
    });
    if (!ok) return;
    await guarded('Applying…', async () => {
      try {
        const body: Record<string, unknown> = { ...changeIdentity(session), confirm: true };
        if (commit) Object.assign(body, { head: session.git!.head, branch: session.git!.branch, commit_message: options.message.trim() });
        const result = await api.post<ChangeMutationResult>(changePath(session, '/apply'), body);
        if (result?.commit?.state === 'failed') toast('Applied, but the commit failed.', 'warning', result.commit.error);
        else toast(result?.commit?.state === 'committed' ? 'Applied and committed.' : 'Applied.', 'success');
        for (const warning of result?.warnings ?? []) toast(warning, 'warning');
        await refreshAll();
      } catch (err) {
        fail('Apply failed', err);
      }
    });
  }

  /** Accepts a task in its own worktree: its reviewed version is committed on the task branch. */
  async function accept(session: LoadedSession, message: string) {
    const branch = session.worktree!.branch;
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
        const body = { ...changeIdentity(session), confirm: true, head: session.worktree!.base, branch, commit_message: message.trim() };
        const result = await api.post<ChangeMutationResult>(changePath(session, '/apply'), body);
        toast(`Accepted and committed on ${branch}.`, 'success');
        for (const warning of result?.warnings ?? []) toast(warning, 'warning');
        await refreshAll();
      } catch (err) {
        fail('Accept failed', err);
      }
    });
  }

  async function undo(session: LoadedSession) {
    if (!proposalReadiness(session).canUndo) return;
    const ok = await confirm({
      title: 'Undo these changes?',
      body: `Restores the files changed by “${session.title || session.id}” after verifying they still match the applied version.${session.commit?.state === 'committed' ? ' The commit stays in history.' : ''}${runningTasksNote(session, (session.changes ?? []).map(change => change.path))}`,
      confirmLabel: 'Undo',
      danger: true,
    });
    if (!ok) return;
    await guarded('Undoing…', async () => {
      try {
        await api.post(changePath(session, '/undo'), { ...changeIdentity(session), confirm: true });
        toast('Changes undone.', 'success');
        await refreshAll();
      } catch (err) {
        fail('Undo failed', err);
      }
    });
  }

  return { markViewed, runChecks, addComment, deleteComment, approve, overrideReview, reject, apply, undo };
}
