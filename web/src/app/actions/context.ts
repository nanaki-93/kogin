// The shared context every area's actions are built from: the query client,
// the toast and confirm dialogs, the one-mutation-at-a-time lock and the
// helpers that record and refresh what an action changed.

import { useQueryClient } from '@tanstack/react-query';
import { errorMessage, withWorktree } from '@/api/client';
import type { ChangeSession, LoadedSession, ModelsCurrent } from '@/api/types';
import { useFeedback } from '@/components/overlays';
import { keys } from '@/api/hooks';
import { busyLabel, setBusy } from './busy';

export const changePath = (session: LoadedSession, suffix: string) => withWorktree('/api/projects/current/changes/' + encodeURIComponent(session.id) + suffix, session.checkout.worktreePath);

export function useActionContext() {
  const client = useQueryClient();
  const { confirm, toast } = useFeedback();

  async function guarded<T>(label: string, fn: () => Promise<T>): Promise<T | undefined> {
    if (busyLabel) {
      toast(`Wait for “${busyLabel.replace(/…$/, '')}” to finish.`, 'warning');
      return undefined;
    }
    setBusy(label);
    try {
      return await fn();
    } finally {
      setBusy('');
    }
  }

  function fail(prefix: string, err: unknown) {
    toast(prefix, 'danger', errorMessage(err));
  }

  function setSession(next: ChangeSession | null | undefined, previous: LoadedSession) {
    if (!next) return;
    const merged: LoadedSession = { ...next, checkout: previous.checkout };
    client.setQueryData(keys.session(previous.id), merged);
    void client.invalidateQueries({ queryKey: ['changes'] });
  }

  function refreshAll() {
    return client.invalidateQueries();
  }

  async function confirmRemote(scope: string, action: string): Promise<{ ok: boolean; remote: boolean }> {
    const models = client.getQueryData<ModelsCurrent>(keys.models);
    const model = models?.scopes?.[scope];
    if (!model?.remote_provider) return { ok: true, remote: false };
    const ok = await confirm({
      title: 'Send code to a remote model?',
      body: `${action} uses ${model.model || scope} at ${model.provider_origin || 'a remote provider'}. Bounded project context for this request leaves your machine.`,
      confirmLabel: 'Send request',
    });
    return { ok, remote: true };
  }

  return { client, confirm, toast, guarded, fail, setSession, refreshAll, confirmRemote };
}

/** What each area's actions are built from. */
export type ActionContext = ReturnType<typeof useActionContext>;
