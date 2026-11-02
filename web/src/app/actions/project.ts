// Project actions: execution trust, reindexing, opening a project and
// deleting files Kasuri set aside.

import { api, withWorktree } from '@/api/client';
import type { ExecutionTrust, Project, QuarantinedFile } from '@/api/types';
import { navigate } from '@/lib/router';
import type { ActionContext } from './context';

export function projectActions(context: ActionContext) {
  const { client, confirm, toast, guarded, fail, refreshAll } = context;

  // ---------------------------------------------------------------- project

  async function setTrust(project: Project, worktreePath: string, trust: ExecutionTrust | undefined, next: boolean) {
    const commands = (trust?.commands ?? []).map(argv => argv.join(' '));
    const ok = await confirm({
      title: next ? 'Allow this project to run checks?' : 'Stop running project checks?',
      body: next ? `Checks and agent verification run these commands in an isolated copy of the project: ${commands.join(' · ') || 'the project check commands'}.` : 'Checks, scans and agent verification stop running project code.',
      confirmLabel: next ? 'Allow' : 'Revoke',
      danger: !next,
    });
    if (!ok) return;
    await guarded('Saving trust…', async () => {
      try {
        await api.post(withWorktree('/api/projects/current/execution-trust', worktreePath), { project_revision: project.project_revision, confirm: next });
        toast(next ? 'Checks allowed for this project.' : 'Trust revoked.', 'success');
        void client.invalidateQueries({ queryKey: ['trust'] });
      } catch (err) {
        fail('Could not update trust', err);
      }
    });
  }

  async function reindex(project: Project, worktreePath: string) {
    await guarded('Reindexing…', async () => {
      try {
        await api.post(withWorktree('/api/projects/current/reindex', worktreePath), { project_revision: project.project_revision });
        toast('Project reindexed.', 'success');
        await refreshAll();
      } catch (err) {
        fail('Reindex failed', err);
      }
    });
  }

  async function openProject(path: string) {
    await guarded('Opening project…', async () => {
      try {
        try {
          await api.post('/api/projects/restore', { project_path: path });
        } catch {
          await api.post('/api/projects/import', { project_path: path });
        }
        navigate('/', { wt: undefined });
        toast(`Opened ${path}`, 'success');
        await refreshAll();
      } catch (err) {
        fail('Could not open the project', err);
      }
    });
  }

  /** Deletes a file Kasuri set aside because it could not read it, with its record. */
  async function deleteSetAside(project: Project, worktreePath: string, file: QuarantinedFile) {
    if (!file.quarantined_as) return;
    const ok = await confirm({
      title: 'Delete the set-aside file?',
      body: `${file.quarantined_as} is deleted for good. Kasuri no longer reads it, so work is not affected; only the history it held is lost.`,
      confirmLabel: 'Delete file',
      danger: true,
    });
    if (!ok) return;
    await guarded('Deleting the set-aside file…', async () => {
      try {
        await api.post(withWorktree('/api/projects/current/quarantine/delete', worktreePath), { project_id: project.project_id, project_revision: project.project_revision, quarantined_as: file.quarantined_as });
        toast('Set-aside file deleted.', 'success');
      } catch (err) {
        fail('Could not delete the set-aside file', err);
      } finally {
        void client.invalidateQueries({ queryKey: ['quarantine'] });
      }
    });
  }

  return { setTrust, reindex, openProject, deleteSetAside };
}
