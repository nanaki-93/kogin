// Issue actions: draft a task that fixes an issue, dismiss or reopen one.

import { api, query, withWorktree } from '@/api/client';
import type { Finding, IssueTaskDraft, Project } from '@/api/types';
import { openNewTask } from '@/app/store';
import type { ActionContext } from './context';

export function issueActions(context: ActionContext) {
  const { client, toast, guarded, fail } = context;

  /**
   * Opens the composer with the task the daemon drafts for an issue: its
   * file and conventional test, criteria and the linked finding, so merging
   * the task marks the issue fixed.
   */
  async function fixIssue(project: Project | undefined, worktreePath: string, finding: Finding) {
    if (!project) return;
    try {
      const draft = await api.get<IssueTaskDraft>(withWorktree(`/api/projects/current/findings/${encodeURIComponent(finding.id)}/task` + query({ project_revision: project.project_revision }), worktreePath));
      openNewTask({ findingTargets: draft.finding_targets?.length ? draft.finding_targets : undefined, issues: draft.issues?.length ? draft.issues : undefined, title: draft.title, kind: draft.kind, description: draft.description, paths: draft.paths, criteria: draft.acceptance_criteria });
    } catch (err) {
      fail('Could not draft a task for this issue', err);
    }
  }

  /**
   * Opens the composer with one task the daemon drafts for several open
   * issues of a file: each issue in the description and as a step of its own
   * (its worktree fixes them one at a time), their criteria, and links to all.
   */
  async function fixIssues(project: Project | undefined, worktreePath: string, findings: Finding[]) {
    if (!project || !findings.length) return;
    if (findings.length === 1) return fixIssue(project, worktreePath, findings[0]);
    const params = new URLSearchParams(findings.map(finding => ['id', finding.id]));
    params.set('project_revision', project.project_revision);
    try {
      const draft = await api.get<IssueTaskDraft>(withWorktree(`/api/projects/current/findings/task?${params}`, worktreePath));
      openNewTask({ findingTargets: draft.finding_targets?.length ? draft.finding_targets : undefined, issues: draft.issues?.length ? draft.issues : undefined, title: draft.title, kind: draft.kind, description: draft.description, paths: draft.paths, criteria: draft.acceptance_criteria });
    } catch (err) {
      fail('Could not draft a task for these issues', err);
    }
  }

  async function triageFinding(project: Project, worktreePath: string, finding: Finding, status: 'dismissed' | 'open') {
    await guarded('Updating issue…', async () => {
      try {
        await api.patch(withWorktree('/api/projects/current/findings/' + encodeURIComponent(finding.id), worktreePath), { project_revision: project.project_revision, status });
        toast(status === 'dismissed' ? 'Issue dismissed.' : 'Issue reopened.', 'success');
        void client.invalidateQueries({ queryKey: ['findings'] });
      } catch (err) {
        fail('Could not update the issue', err);
      }
    });
  }

  return { fixIssue, fixIssues, triageFinding };
}
