// Closing a task that has nothing to fix, as its agents or its plan found.

import { Archive } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { openWorkspace } from '@/app/checkout';
import { closeDismissals, closeLabel } from '@/lib/readiness';
import { Button } from '@/components/ui';

/** Closes the task, dismissing the issues found not to hold, then leaves its page. */
export function CloseTask({ session, variant = 'secondary', size = 'sm' }: { session: LoadedSession; variant?: 'primary' | 'secondary'; size?: 'sm' | 'md' }) {
  const actions = useActions();
  const busy = useBusy();
  return (
    <Button size={size} variant={variant} icon={Archive} onClick={() => void actions.closeTask(session, () => openWorkspace('/tasks'))} disabled={Boolean(busy)}>
      {closeLabel(closeDismissals(session).length)}
    </Button>
  );
}
