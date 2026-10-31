// The module store behind the actions: the one mutation that runs at a time
// and the revision a task is waiting for.

import { useSyncExternalStore } from 'react';

// ------------------------------------------------------------------ busy state

export let busyLabel = '';
const listeners = new Set<() => void>();
export function setBusy(label: string) {
  busyLabel = label;
  listeners.forEach(fn => fn());
}

export function useBusy(): string {
  return useSyncExternalStore(
    fn => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => busyLabel,
  );
}

// ------------------------------------------------------------ revision state

/** A revision request in flight: when it started and how to stop waiting for it. */
export interface PendingRevision {
  sessionId: string;
  since: number;
  stop: () => void;
}

export let pendingRevision: PendingRevision | null = null;
const revisionListeners = new Set<() => void>();
export function setPendingRevision(next: PendingRevision | null) {
  pendingRevision = next;
  revisionListeners.forEach(fn => fn());
}

/** The revision a task is waiting for, so its page can show the time and Stop. */
export function usePendingRevision(): PendingRevision | null {
  return useSyncExternalStore(
    fn => {
      revisionListeners.add(fn);
      return () => revisionListeners.delete(fn);
    },
    () => pendingRevision,
  );
}
