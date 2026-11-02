// Starts the agents on a task, or runs them again from a chosen start. It is
// offered whenever no run is active; when the daemon would refuse a run, the
// button stays visible, disabled, and says why.

import { Play } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { useTrust } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { workflowBlocker } from '@/lib/workflow';
import { Button, Tip } from '@/components/ui';

export function RunAgents({ session, variant = 'secondary', size }: { session: LoadedSession; variant?: 'primary' | 'secondary' | 'ghost'; size?: 'sm' }) {
  const actions = useActions();
  const busy = useBusy();
  const trust = useTrust().data;
  const blocker = workflowBlocker(session, Boolean(trust?.trusted));
  const first = !(session.changes ?? []).length;
  if (!session.worktree) return null; // Made in the main checkout: read and delete only.
  return (
    <Tip label={blocker || (first ? 'Agents write the change, tests, and review it' : 'Choose where the agents start: this version as it is, new tests, or the change written again')}>
      <span>
        <Button variant={variant} size={size} icon={Play} onClick={() => void actions.startAgents(session)} disabled={Boolean(blocker) || Boolean(busy)}>
          {session.workflow?.status === 'paused' ? 'Resume agents' : first ? 'Start agents' : 'Run agents again'}
        </Button>
      </span>
    </Tip>
  );
}
