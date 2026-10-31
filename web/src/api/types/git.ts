// Git: the workspace, its worktrees and branches, merges and their checks.

import type { GitIdentity, ProjectIdentity } from './project';
import type { ChangeHistoryEntry, Check } from './tasks';

/** A Git mutation's outcome; a merge also names the commit it started from. */
export interface GitMutationResult {
  state: string;
  previous_head?: string;
  repository?: { head: string; branch: string };
  index?: ProjectIdentity;
  error?: string;
  warnings?: string[];
}

/** The tests and lint of the subprojects a merge changed, run on the merged result. */
export interface MergeChecks {
  from: string;
  to: string;
  outcome: 'passed' | 'failed' | 'unavailable' | string;
  summary: string;
  changed_paths: number;
  checks: Check[];
}

export interface GitWorktree extends GitIdentity {
  path: string;
  current?: boolean;
  locked?: boolean;
  prunable?: boolean;
  dirty?: boolean;
  mergeable?: boolean;
  integration?: string;
  overlap_paths?: string[];
  completed?: ChangeHistoryEntry[];
  sessions?: ChangeHistoryEntry[];
  error?: string;
}

export interface GitBranch {
  name: string;
  head: string;
  worktree_path?: string;
}

export interface GitWorkspace extends GitIdentity, ProjectIdentity {
  available: boolean;
  root: string;
  dirty: boolean;
  merge_head?: string;
  operation?: string;
  conflicts: string[];
  branches?: GitBranch[];
  worktrees: GitWorktree[];
}

export interface MergePreview {
  diff?: string;
  commits?: string;
  conflicts?: string[];
  files?: string[];
  state?: string;
}
