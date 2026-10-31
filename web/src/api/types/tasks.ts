// Tasks: their proposals, diffs, checks, comments, plans and history.

import type { FindingTarget } from './issues';
import type { GitIdentity, ProjectIdentity } from './project';
import type { ChangeWorkflow } from './workflow';

export type CheckState = 'passed' | 'failed' | 'running' | 'skipped' | 'unavailable' | 'canceled' | 'pending' | string;

export interface Check {
  name: string;
  required: boolean;
  state: CheckState;
  command?: string[];
  output?: string;
  exit_code?: number;
  duration_ms?: number;
  outcome?: string;
}

export interface DiffLine {
  kind: 'context' | 'added' | 'removed' | 'hunk' | string;
  old_line?: number;
  new_line?: number;
  text: string;
}

export interface FileDiff {
  old_path?: string;
  new_path?: string;
  lines: DiffLine[];
}

export interface FileChange {
  path: string;
  /** added, modified, deleted or renamed; absent in proposals that only add or modify files. */
  status?: 'added' | 'modified' | 'deleted' | 'renamed';
  /** Where a renamed file came from. */
  old_path?: string;
  content: string;
  hash: string;
  summary?: string;
  criteria?: number[];
  diff: FileDiff;
}

export interface ChangeTarget {
  path: string;
  hash: string;
  exists: boolean;
  /** A test file by convention or by the project's AGENTS.md test_files. */
  test?: boolean;
}

export interface ChangeCommit extends Partial<GitIdentity> {
  state: string;
  message?: string;
  oid?: string;
  error?: string;
}

/** A reviewer comment on one line of a proposal version. */
export interface ChangeComment {
  id: string;
  path: string;
  side: 'new' | 'old';
  line: number;
  excerpt: string;
  text: string;
  /** The proposal the comment was written on. */
  hash: string;
  /** The revision whose repair brief carried it; absent while open. */
  sent_revision?: number;
  created_at: string;
}

export interface ChangeMessage {
  role: string;
  content: string;
}

export interface PlanEvidence {
  path: string;
  start_line?: number;
  end_line?: number;
  quote?: string;
  reason?: string;
}

export interface ChangePlan {
  problem?: string;
  approach?: string;
  evidence?: PlanEvidence[];
  assumptions?: string[];
  non_goals?: string[];
  read_paths?: string[];
  hash?: string;
  approved_hash?: string;
}

export interface UndoAvailability {
  available: boolean;
  reason: string;
}

export interface ChangeSession extends ProjectIdentity {
  id: string;
  kind: string;
  title: string;
  acceptance_criteria: string[];
  revision: number;
  hash: string;
  /** Set when the task owns its worktree: its proposal is that worktree's diff against base, accepted as a commit on branch. */
  worktree?: { base: string; branch: string };
  state: string;
  freshness: string;
  targets: ChangeTarget[];
  changes: FileChange[];
  pinned_tests?: FileChange[];
  review_diff?: FileChange[];
  messages: ChangeMessage[];
  checks: Check[];
  reviewed_hash?: string;
  review_notes?: string;
  comments?: ChangeComment[];
  /** Why the proposal is stale: the scope file that changed. */
  stale_reason?: string;
  /** The project changed since the checks ran; they must run again. */
  checks_outdated?: boolean;
  reviewed_files?: string[];
  file_review_hash?: string;
  last_reviewed_hash?: string;
  repair_attempts?: number;
  workflow?: ChangeWorkflow;
  plan?: ChangePlan;
  finding_targets?: FindingTarget[];
  git?: GitIdentity;
  commit?: ChangeCommit;
  worktree_path?: string;
  updated_at: string;
}

/** A task the daemon captured in the worktree it created for it. */
export interface ChangeOpened extends ChangeSession {
  worktree_path: string;
  branch: string;
  warnings: string[];
}

/** A change session as the UI tracks it: where it lives and its checkout identity. */
export interface LoadedSession extends ChangeSession {
  checkout: CheckoutRef;
  undo?: UndoAvailability;
}

export interface CheckoutRef extends ProjectIdentity {
  worktreePath: string;
}

export interface ChangeHistoryEntry extends ProjectIdentity {
  id: string;
  kind: string;
  title: string;
  revision: number;
  hash: string;
  state: string;
  freshness: string;
  commit?: ChangeCommit;
  updated_at: string;
  paths?: string[];
  workflow_status?: string;
  workflow_reason?: string;
  worktree_path?: string;
  finding_targets?: FindingTarget[];
}

/** A task row in lists: the history entry plus the checkout that owns it. */
export interface TaskEntry extends ChangeHistoryEntry {
  worktreePath: string;
  branch?: string;
}

/** What re-analyzing a stale task decided: refreshed (still_valid) or deleted (not_valid). */
export interface ChangeReanalysis {
  outcome: 'still_valid' | 'not_valid';
  /** The model's explanation: a suggestion, not a verified finding. */
  reason: string;
  session?: ChangeSession;
  deleted?: string;
  dismissed_issues?: number;
  warnings: string[];
}

export interface ChangeMutationResult extends ProjectIdentity {
  session_id: string;
  state: string;
  hash: string;
  undo_available: boolean;
  warnings: string[];
  commit?: ChangeCommit;
}
