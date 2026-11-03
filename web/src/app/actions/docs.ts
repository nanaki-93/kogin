// Actions that explain the code: refresh the docs and ask the codebase a
// question.

import { api, query, withWorktree } from '@/api/client';
import type { AgentSettings, AskAnswer, DocsRefresh, Model, ModelsCurrent, Project } from '@/api/types';
import { keys } from '@/api/hooks';
import type { ActionContext } from './context';

export function docsActions(context: ActionContext) {
  const { client, confirm, guarded, fail } = context;

  /** Starts writing the docs' summaries and overview again; the Docs page shows how it ends. */
  async function refreshDocs(project: Project, worktreePath: string) {
    const model = client.getQueryData<ModelsCurrent>(keys.models)?.scopes?.analyze;
    const remote = Boolean(model?.remote_provider);
    const ok = await confirm({
      title: 'Refresh the docs?',
      body: `${model?.model || 'The analyze model'} summarizes again the components whose files' summaries changed, then writes the project overview again: at most one request per component and one for the overview. It runs in the background.${remote ? ` File summaries and bounded project context go to ${model?.provider_origin || 'a remote provider'}.` : ''}`,
      confirmLabel: 'Refresh docs',
    });
    if (!ok) return;
    await guarded('Starting the docs refresh…', async () => {
      try {
        await api.post<DocsRefresh>(withWorktree('/api/projects/current/docs/refresh', worktreePath), { project_id: project.project_id, project_revision: project.project_revision, confirm_remote_provider: remote });
        await client.invalidateQueries({ queryKey: ['overview'] });
      } catch (err) {
        fail('The docs refresh could not start', err);
      }
    });
  }

  /**
   * Asks the Review role's agent a question about the code after consent. It
   * only reads, so it does not hold the one-mutation-at-a-time lock.
   */
  async function askCodebase(project: Project, worktreePath: string, question: string): Promise<AskAnswer | null> {
    let model: Model | undefined;
    try {
      const settings = await api.get<AgentSettings>(withWorktree('/api/projects/current/agent-settings' + query({ project_id: project.project_id, project_revision: project.project_revision }), worktreePath));
      model = settings.role_models?.review;
    } catch {
      model = undefined;
    }
    const remote = model?.remote_provider !== false;
    if (remote) {
      const ok = await confirm({
        title: 'Ask the codebase?',
        body: `${model?.model || 'The Review role’s model'}${model?.provider_origin ? ` at ${model.provider_origin}` : ''} reads the project's files through Kogin's read-only tools to answer, then cites the lines it used. File contents leave your machine; it can take a few minutes and changes nothing.`,
        confirmLabel: 'Ask',
      });
      if (!ok) return null;
    }
    try {
      return await api.post<AskAnswer>(withWorktree('/api/projects/current/ask', worktreePath), { project_id: project.project_id, project_revision: project.project_revision, question, confirm_remote_provider: remote });
    } catch (err) {
      fail('The question could not be answered', err);
      return null;
    }
  }

  return { refreshDocs, askCodebase };
}
