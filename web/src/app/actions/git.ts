// Git actions: preview and merge a worktree, check the merged result, remove
// worktrees and abort a merge.

import { api, errorMessage, query } from '@/api/client';
import type { GitMutationResult, GitWorkspace, GitWorktree, MergeChecks, MergePreview, Project } from '@/api/types';
import { mergeRequest, worktreeDiscardable, worktreeRemovable, worktreeTasks } from '@/lib/workflow';
import { currentRoute } from '@/lib/router';
import { getUI } from '@/app/store';
import { forgetCheckouts, openWorkspace } from '@/app/checkout';
import { mergeToCheck, runMergeChecks } from '@/app/mergeChecks';
import { plural, unique } from '@/lib/util';
import type { ActionContext } from './context';

export function gitActions(context: ActionContext) {
  const { client, confirm, toast, guarded, fail, refreshAll } = context;

  // ---------------------------------------------------------------- git

  async function previewMerge(workspace: GitWorkspace, worktree: GitWorktree, main: Project): Promise<MergePreview | null> {
    try {
      return await api.post<MergePreview>('/api/projects/current/git/merge-preview', mergeRequest(workspace, worktree, main));
    } catch (err) {
      fail('Merge preview failed', err);
      return null;
    }
  }

  /** Merges a worktree; the Branches page setting decides whether the worktree and branch go too. */
  async function merge(workspace: GitWorkspace, worktree: GitWorktree, main: Project) {
    const overlap = worktree.overlap_paths ?? [];
    const remove = getUI().removeWorktreeOnMerge;
    const ok = await confirm({
      title: `Merge ${worktree.branch || 'worktree'} into ${workspace.branch || 'the current branch'}?`,
      body: `Merges ${worktree.head.slice(0, 7)}, marks its linked issues done, then ${remove ? 'removes the worktree and its branch' : 'keeps the worktree and its branch (remove them here later)'}.${overlap.length ? ` ${plural(overlap.length, 'path')} changed on both branches: ${overlap.slice(0, 3).join(', ')}${overlap.length > 3 ? ', …' : ''}.` : ''} Conflicts stop the merge so you can resolve or abort it. Running tasks keep going unless the merge changes their files.`,
      confirmLabel: 'Merge',
    });
    if (!ok) return;
    await guarded('Merging…', async () => {
      try {
        const result = await api.post<GitMutationResult>('/api/projects/current/git/merge', { ...mergeRequest(workspace, worktree, main), keep_worktree: remove ? undefined : true });
        if (result?.state === 'conflicts') toast('The merge stopped with conflicts. Resolve them in your editor, or abort.', 'warning');
        else toast(`Merged ${worktree.branch || worktree.path}.`, 'success');
        for (const warning of result?.warnings ?? []) toast(warning, 'warning');
        await refreshAll();
        const merged = mergeToCheck(result);
        if (merged) void runMergeChecks(worktree.branch || worktree.path, merged, reportMergeChecks);
      } catch (err) {
        fail('Merge failed', err);
      }
    });
  }

  function reportMergeChecks(result: MergeChecks) {
    if (result.outcome === 'passed') toast('The checks pass on the merged result.', 'success');
    else if (result.outcome === 'failed') toast('Checks failed after the merge', 'danger', result.summary);
    else toast('The merged result could not be checked', 'warning', result.summary);
  }

  /**
   * Removes a worktree whose commits the main branch already has, with its
   * branch. One with applied tasks goes through cleanup-merged, which also
   * marks their linked issues done (as after a merge made outside Kasuri);
   * one without tasks is discarded.
   */
  async function removeWorktree(workspace: GitWorkspace, worktree: GitWorktree, main: Project) {
    const tasks = worktreeTasks(worktree).length;
    const ok = await confirm({
      title: `Remove the worktree ${worktree.branch || worktree.path}?`,
      body: `Deletes its checkout and its branch; ${workspace.branch || 'the main branch'} already has all its commits.${tasks ? ` Its linked issues are marked done, and the history of its ${plural(tasks, 'applied task')} goes with it.` : ''}`,
      confirmLabel: 'Remove worktree',
      danger: true,
    });
    if (!ok) return;
    await guarded('Removing worktree…', async () => {
      try {
        const result = await api.post<{ state: string; error?: string; warnings?: string[] }>(`/api/projects/current/git/${tasks ? 'cleanup-merged' : 'discard-worktree'}`, mergeRequest(workspace, worktree, main));
        if (result.state === 'failed') toast('Could not remove the worktree', 'danger', result.error);
        else toast(`Removed ${worktree.branch || worktree.path}.`, 'success');
        for (const warning of result.warnings ?? []) toast(warning, 'warning');
        leaveRemovedCheckout([worktree.path]);
      } catch (err) {
        fail('Could not remove the worktree', err);
      }
      await refreshAll();
    });
  }

  /**
   * Removes the worktrees in `paths` that were left with no tasks and nothing
   * to merge, such as the one created for a task just deleted. Returns their
   * branches; a worktree that cannot be removed stays and is reported.
   */
  async function removeEmptyWorktrees(paths: string[]): Promise<string[]> {
    const wanted = unique(paths.filter(Boolean));
    if (!wanted.length) return [];
    const removed: string[] = [];
    try {
      const main = await api.get<Project>('/api/projects/current');
      const workspace = await api.get<GitWorkspace>('/api/projects/current/git/workspace' + query({ project_id: main.project_id, project_revision: main.project_revision }));
      for (const tree of workspace.worktrees ?? []) {
        if (!wanted.includes(tree.path) || worktreeTasks(tree).length || !worktreeRemovable(tree)) continue;
        const result = await api.post<{ state: string; error?: string; removed_worktree?: string }>('/api/projects/current/git/discard-worktree', mergeRequest(workspace, tree, main));
        if (result.removed_worktree) removed.push(tree.path);
        else toast(`The worktree ${tree.branch || tree.path} was kept`, 'warning', result.error);
      }
    } catch (err) {
      toast('A worktree left without tasks was kept', 'warning', errorMessage(err));
    }
    leaveRemovedCheckout(removed);
    void client.invalidateQueries({ queryKey: ['git'] });
    void client.invalidateQueries({ queryKey: ['findings'] });
    return removed;
  }

  /**
   * Discards a worktree left with no tasks together with its work: its
   * checkout, its uncommitted changes and its branch, commits the main branch
   * lacks included. Asks first, naming what is lost.
   */
  async function discardWorktree(workspace: GitWorkspace, worktree: GitWorktree, main: Project, why = 'No task is left on it.'): Promise<boolean> {
    const lost = [worktree.dirty ? 'its uncommitted changes' : '', worktree.integration !== 'merged' ? `the commits ${workspace.branch || 'the main branch'} does not have` : ''].filter(Boolean).join(' and ');
    const ok = await confirm({
      title: `Discard the worktree ${worktree.branch || worktree.path}?`,
      body: `${why} Its checkout and its branch are deleted${lost ? `, with ${lost}` : ''}. This cannot be undone; keep it to merge or recover that work with Git.`,
      confirmLabel: 'Discard worktree',
      cancelLabel: 'Keep it',
      danger: true,
    });
    if (!ok) return false;
    const discarded = await guarded('Discarding worktree…', async () => {
      try {
        const result = await api.post<GitMutationResult>('/api/projects/current/git/discard-worktree', { ...mergeRequest(workspace, worktree, main), discard_work: true });
        if (result.state === 'failed') {
          toast('Could not discard the worktree', 'danger', result.error);
          return false;
        }
        toast(`Discarded ${worktree.branch || worktree.path}.`, 'success');
        for (const warning of result.warnings ?? []) toast(warning, 'warning');
        leaveRemovedCheckout([worktree.path]);
        return true;
      } catch (err) {
        fail('Could not discard the worktree', err);
        return false;
      } finally {
        await refreshAll();
      }
    });
    return Boolean(discarded);
  }

  /**
   * Offers to discard each worktree in `paths` that deleting its tasks left
   * with work of its own (removeEmptyWorktrees keeps those), such as a branch
   * whose merge was aborted. Returns the worktrees discarded.
   */
  async function discardAbandonedWorktrees(paths: string[]): Promise<string[]> {
    const wanted = unique(paths.filter(Boolean));
    if (!wanted.length) return [];
    let main: Project, workspace: GitWorkspace;
    try {
      main = await api.get<Project>('/api/projects/current');
      workspace = await api.get<GitWorkspace>('/api/projects/current/git/workspace' + query({ project_id: main.project_id, project_revision: main.project_revision }));
    } catch {
      return [];
    }
    const discarded: string[] = [];
    for (const tree of workspace.worktrees ?? []) {
      if (wanted.includes(tree.path) && worktreeDiscardable(tree) && (await discardWorktree(workspace, tree, main, 'Its tasks are deleted, but it still holds their work.'))) discarded.push(tree.path);
    }
    return discarded;
  }

  /** Leaves a removed checkout: its pages return to the main one, a task page to the task list. */
  function leaveRemovedCheckout(paths: string[]) {
    forgetCheckouts(paths);
    const route = currentRoute();
    const wt = route.params.get('wt');
    if (wt && paths.includes(wt)) openWorkspace(route.path.startsWith('/tasks/') ? '/tasks' : route.path);
  }

  async function abortMerge(workspace: GitWorkspace, main: Project) {
    if (!workspace.merge_head) return;
    if (!(await confirm({ title: 'Abort the merge?', body: 'Discards conflict resolutions and restores the branch to its state before the merge.', confirmLabel: 'Abort merge', danger: true }))) return;
    await guarded('Aborting merge…', async () => {
      try {
        await api.post('/api/projects/current/git/abort-merge', { project_id: main.project_id, project_revision: main.project_revision, head: workspace.head, branch: workspace.branch, merge_head: workspace.merge_head, confirm: true, confirm_execution: true });
        toast('Merge aborted.', 'success');
        await refreshAll();
      } catch (err) {
        fail('Abort failed', err);
      }
    });
  }

  return { previewMerge, merge, removeWorktree, removeEmptyWorktrees, discardWorktree, discardAbandonedWorktrees, abortMerge };
}
