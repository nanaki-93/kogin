// Issues (findings), where they point and the tasks that fix them.

import type { ProjectIdentity } from './project';

/** GET findings/{id}/task: the task the daemon drafts to fix one issue. */
export interface IssueTaskDraft {
  kind: string;
  title: string;
  description: string;
  paths: string[];
  acceptance_criteria: string[];
  finding_targets?: FindingTarget[];
  /** For several issues: each one, fixed in a step of its own in the task's worktree. */
  issues?: TaskIssue[];
}

/** An issue a task fixes in its own step, with that step's progress. */
export interface TaskIssue {
  finding_id: string;
  path: string;
  title: string;
  detail: string;
  criteria?: string[];
  /** pending, fixing, fixed, not_fixed or does_not_hold; absent on a draft. */
  status?: string;
  /** Why it was not fixed or does not hold, or that an earlier step fixed it. */
  reason?: string;
  /** The commit of its fix. */
  commit?: string;
}

export interface FindingTarget {
  project_id: string;
  kind: string;
  id: string;
  path: string;
}

export interface SourceAnchor {
  path: string;
  start_line?: number;
  end_line?: number;
  /** The function around start_line, as the syntax layer names it. */
  symbol?: string;
}

export interface Finding {
  id: string;
  project_id?: string;
  source: string;
  confidence?: string;
  severity: string;
  title: string;
  message: string;
  rule?: string;
  location: SourceAnchor;
  evidence?: string;
  status: string;
  freshness?: string;
  category?: string;
  task_spec?: { target_path?: string; acceptance_criteria?: string[]; non_goals?: string[] };
  /** The pass that found it: file, security, performance or project. */
  originating_analysis?: string;
  /** Other passes' reports of the same issue, merged into this one. */
  also_reported?: FindingReport[];
  /** The dismissed issue this one repeats in new words; it was dismissed for that reason. */
  seen_before?: { id: string; title: string };
  /** In a test file. */
  test?: boolean;
}

/** Another analysis pass's report of an issue. */
export interface FindingReport {
  analysis: string;
  title: string;
  severity: string;
  line?: number;
}

export interface FindingsResponse extends ProjectIdentity {
  findings: Finding[];
}

/** A file outside a fix's folders its agent may change, because the fix needs it. */
export interface ScopeGrant {
  path: string;
  reason: string;
  /** The finding of the issue step that asked for it. */
  issue?: string;
}

/** Work the testing agent found the change cannot cover: a suggested task of its own. */
export interface FollowUp {
  title: string;
  detail: string;
  path?: string;
}
