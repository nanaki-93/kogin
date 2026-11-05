// Health actions: save the project profile, run the tests with coverage, and
// open the composer with a task that improves the health.

import { api } from '@/api/client';
import type { HealthTask, Project, ProjectProfile } from '@/api/types';
import { keys } from '@/api/hooks';
import { openNewTask } from '@/app/store';
import type { ActionContext } from './context';

export function healthActions(context: ActionContext) {
  const { client, toast, guarded, fail } = context;

  /** Saves the profile; the issue list applies it at once. */
  async function saveProfile(project: Project, profile: ProjectProfile): Promise<ProjectProfile | null> {
    return (await guarded('Saving the project profile…', async () => {
      try {
        const { saved: _saved, ...fields } = profile;
        const result = await api.post<ProjectProfile>('/api/projects/current/profile', { project_revision: project.project_revision, profile: fields });
        client.setQueryData([...keys.profile, project.project_revision], result);
        void client.invalidateQueries({ queryKey: ['findings'] });
        void client.invalidateQueries({ queryKey: keys.health });
        toast('Project profile saved. Issues are counted against it now; the next analysis also reads it.', 'success');
        return result;
      } catch (err) {
        fail('Could not save the project profile', err);
        return null;
      }
    })) ?? null;
  }

  /** Starts the tests in a copy of the project; the health polls until they finish. */
  async function runHealthTests(project: Project): Promise<boolean> {
    return (await guarded('Starting the tests…', async () => {
      try {
        await api.post('/api/projects/current/health/tests', { project_revision: project.project_revision });
        await client.invalidateQueries({ queryKey: keys.health });
        return true;
      } catch (err) {
        fail('Could not run the tests', err);
        return false;
      }
    })) ?? false;
  }

  /** Opens the composer with a health task's draft; nothing is saved until it is created. */
  function startHealthTask(task: HealthTask) {
    const draft = task.draft;
    openNewTask({ findingTargets: draft.finding_targets?.length ? draft.finding_targets : undefined, title: draft.title, kind: draft.kind, description: draft.description, paths: draft.paths, criteria: draft.acceptance_criteria });
  }

  return { saveProfile, runHealthTests, startHealthTask };
}
