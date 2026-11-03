// The daemon itself: its status, live events and quarantined records.

import type { BudgetUsage, CostEstimate } from './usage';

/** A record Kogin set aside because it was unreadable, or skipped because a newer version wrote it. */
export interface QuarantinedFile {
  kind: 'agent_journal' | 'task' | 'analysis_run' | string;
  file: string;
  quarantined_as?: string;
  reason: string;
  remedy: string;
  at?: string;
}

export interface QuarantineReport {
  files: QuarantinedFile[];
  skipped: QuarantinedFile[];
}

/** A live update: what changed and, for workflows, the task, stage, status and spend. */
export interface DaemonEvent {
  kind: string;
  session_id?: string;
  workflow_id?: string;
  title?: string;
  stage?: string;
  status?: string;
  usage?: BudgetUsage;
  cost?: CostEstimate;
  /** The worktree a task's event comes from. */
  worktree_path?: string;
}

export interface DaemonStatus {
  status: string;
  version: string;
}
