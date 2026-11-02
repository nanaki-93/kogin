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
  /** pending, fixing, fixed or not_fixed; absent on a draft. */
  status?: string;
  /** Why it was not fixed. */
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
}

export interface FindingsResponse extends ProjectIdentity {
  findings: Finding[];
}
