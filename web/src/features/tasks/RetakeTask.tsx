// A task in its own worktree whose files the main branch changed since it
// started, often by merging another task on the same files: its work, done
// or not, is built on code that is gone. It is re-analyzed against the main
// branch and, if its issue is still there, started again from it.

import { GitMerge, RefreshCcw } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { workflowActive } from '@/lib/readiness';
import { mainChangedMessage } from '@/lib/workflow';
import { Button, Callout } from '@/components/ui';

export function MainChanged({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const change = session.main_changed;
  if (!change) return null;
  const running = workflowActive(session);
  const message = mainChangedMessage(change);
  return (
    <Callout
      tone="warning"
      icon={GitMerge}
      title={message.title}
      action={
        <Button size="sm" variant="secondary" icon={RefreshCcw} onClick={() => void actions.retake(session)} disabled={running || Boolean(busy)} loading={busy === 'Retaking…'}>
          Re-analyze &amp; retake
        </Button>
      }
    >
      {message.body}
      {running ? ' Its agents stop before their next stage.' : ''}
    </Callout>
  );
}
