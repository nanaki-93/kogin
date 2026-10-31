// Setup actions: the project's configuration file, time limits, model tests,
// analysis exclusions and the agent settings that override them.

import { api, withWorktree } from '@/api/client';
import type { AgentPreferences, AgentSettings, AnalysisFileSelection, ProjectSetup, SetupModel, SetupModelTest, SetupScope, SetupTimeouts } from '@/api/types';
import { keys } from '@/api/hooks';
import type { ActionContext } from './context';

export function setupActions(context: ActionContext) {
  const { client, toast, guarded, fail } = context;

  // --------------------------------------------------------------- project setup

  /** Saves the project's configuration file; the daemon applies it at once. */
  async function saveSetup(setup: ProjectSetup, draft: Pick<ProjectSetup, 'models' | 'roles' | 'limits' | 'timeouts'>): Promise<ProjectSetup | null> {
    return (await guarded('Saving the project configuration…', async () => {
      try {
        const saved = await api.post<ProjectSetup>('/api/projects/current/setup', { project_id: setup.project_id, project_revision: setup.project_revision, models: draft.models, roles: draft.roles, limits: draft.limits, timeouts: draft.timeouts });
        client.setQueryData([...keys.setup, setup.project_revision], saved);
        void client.invalidateQueries({ queryKey: ['models'] });
        void client.invalidateQueries({ queryKey: ['settings'] });
        toast('Project configuration saved.', 'success', saved.config_file);
        if (saved.overrides_reset) toast('Agent overrides from Settings → Models & agents were replaced by these settings.', 'info');
        return saved;
      } catch (err) {
        fail('Could not save the project configuration', err);
        return null;
      }
    })) ?? null;
  }

  /** Saves the project's time limits alone and applies them at once. */
  async function saveTimeouts(setup: ProjectSetup, timeouts: SetupTimeouts): Promise<boolean> {
    return (await guarded('Saving time limits…', async () => {
      try {
        const saved = await api.post<ProjectSetup>('/api/projects/current/setup/timeouts', { project_id: setup.project_id, project_revision: setup.project_revision, timeouts });
        client.setQueryData([...keys.setup, setup.project_revision], saved);
        void client.invalidateQueries({ queryKey: ['models'] });
        toast('Time limits saved.', 'success', saved.config_file);
        return true;
      } catch (err) {
        fail('Could not save the time limits', err);
        return false;
      }
    })) ?? false;
  }

  /** Keeps the daemon's configuration for this project and stops offering setup. */
  async function skipSetup(setup: ProjectSetup): Promise<boolean> {
    return (await guarded('Skipping setup…', async () => {
      try {
        const skipped = await api.post<ProjectSetup>('/api/projects/current/setup/skip', { project_id: setup.project_id, project_revision: setup.project_revision });
        client.setQueryData([...keys.setup, setup.project_revision], skipped);
        return true;
      } catch (err) {
        fail('Could not skip setup', err);
        return false;
      }
    })) ?? false;
  }

  /** Sends one short request to a model as setup would configure it. */
  async function testSetupModel(setup: ProjectSetup, scope: SetupScope, model: SetupModel): Promise<SetupModelTest | null> {
    try {
      return await api.post<SetupModelTest>('/api/projects/current/setup/test-model', { project_id: setup.project_id, project_revision: setup.project_revision, scope, model });
    } catch (err) {
      fail('Could not test the model', err);
      return null;
    }
  }

  /** Saves which files analysis leaves out. */
  async function saveAnalysisSelection(selection: AnalysisFileSelection, excluded: string[]): Promise<boolean> {
    return (await guarded('Saving analysis exclusions…', async () => {
      try {
        const saved = await api.post<AnalysisFileSelection>('/api/projects/current/analysis/selection', { project_id: selection.project_id, project_revision: selection.project_revision, selection_id: selection.selection_id, excluded_paths: excluded });
        client.setQueryData([...keys.selection, selection.project_revision], saved);
        return true;
      } catch (err) {
        fail('Could not save analysis exclusions', err);
        return false;
      }
    })) ?? false;
  }

  // ------------------------------------------------------------ agent settings

  /** Replaces the project's agent roles and budget, or resets them to config.yaml. */
  async function controlAgentSettings(settings: AgentSettings, worktreePath: string, values: AgentPreferences | null) {
    const reset = values === null;
    return guarded(reset ? 'Resetting agent settings…' : 'Saving agent settings…', async () => {
      try {
        const saved = await api.post<AgentSettings>(withWorktree('/api/projects/current/agent-settings/control', worktreePath), {
          project_id: settings.project_id,
          project_revision: settings.project_revision,
          configuration: settings.configuration,
          version: settings.version,
          action: reset ? 'reset' : 'replace',
          confirm: true,
          ...(reset ? {} : { values }),
        });
        client.setQueryData([...keys.settings(worktreePath), settings.project_revision], saved);
        void client.invalidateQueries({ queryKey: keys.settings(worktreePath) });
        toast(reset ? 'Agents follow the configuration again.' : 'Agent settings saved. New runs use them.', 'success');
        return saved;
      } catch (err) {
        fail(reset ? 'Could not reset agent settings' : 'Could not save agent settings', err);
        return null;
      }
    });
  }

  return { saveSetup, saveTimeouts, skipSetup, testSetupModel, saveAnalysisSelection, controlAgentSettings };
}
