// Tasks: their proposals, diffs, checks, comments, plans and history.

import type { FindingTarget, FollowUp, ScopeGrant, TaskIssue } from './issues';
import type { GitIdentity, ProjectIdentity } from './project';
import type { ChangeWorkflow, ReviewItem } from './workflow';

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
  /** The model's reading of the cited lines; provenance, not a verified finding. */
  observation?: string;
  /** The approach step (from 1) that changes the cited lines, and what it does to them. */
  step?: number;
  effect?: 'removed' | 'changed';
  /** The cited lines as the file holds them, from excerpt_line; absent on a plan captured before excerpts. */
  excerpt?: string;
  excerpt_line?: number;
}

/** An approach the plan rejected, and why. */
export interface PlanAlternative {
  approach: string;
  why_not: string;
}

/** A file the plan expects the change to edit or create. */
export interface PlanTouch {
  path: string;
  why: string;
}

/** What the investigation found for one linked issue. */
export interface PlanIssueVerdict {
  finding_id: string;
  verdict: 'holds' | 'already_fixed' | 'does_not_hold' | 'needs_own_task';
  note?: string;
}

export interface ChangePlan {
  /** The problem in one plain sentence, without identifiers; absent on older plans. */
  claim?: string;
  problem?: string;
  approach?: string;
  evidence?: string;
  citations?: PlanEvidence[];
  assumptions?: string[];
  non_goals?: string[];
  read_paths?: string[];
  /** Why the problem happens, naming the code; the citations show it. */
  root_cause?: string;
  alternatives?: PlanAlternative[];
  risks?: string[];
  touches?: PlanTouch[];
  issue_verdicts?: PlanIssueVerdict[];
  hash?: string;
  approved_hash?: string;
  /** Empty for a plan the user wrote at creation; 'investigator' for one the investigation proposed. */
  source?: string;
  /** The model that proposed it, and when. */
  model?: string;
  investigated_at?: string;
}

/** The plan you wrote before the investigator's was shown; kept as written. */
export interface OwnPlan {
  wrong?: string;
  approach?: string;
  risk?: string;
  /** You chose to read the agent's plan without writing one. */
  skipped?: boolean;
  plan_hash: string;
  written_at: string;
  /** Your own mark of how close it was, once you saw both. */
  match?: 'same' | 'partly' | 'missed';
}

export interface ChangeSession extends ProjectIdentity {
  own_plan?: OwnPlan;
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
  /** What each file's change fixes, hunk by hunk, for its reviewer. */
  walkthrough?: ChangeWalkthrough;
  messages: ChangeMessage[];
  checks: Check[];
  reviewed_hash?: string;
  review_notes?: string;
  /** The behavior changes the human accepted with the approved version. */
  accepted_behavior_changes?: ReviewItem[];
  comments?: ChangeComment[];
  /** Why the proposal is stale: the scope file that changed. */
  stale_reason?: string;
  /** For a task in its own worktree: its files the main branch changed since it started. */
  main_changed?: MainChange;
  /** The project changed since the checks ran; they must run again. */
  checks_outdated?: boolean;
  reviewed_files?: string[];
  file_review_hash?: string;
  last_reviewed_hash?: string;
  repair_attempts?: number;
  workflow?: ChangeWorkflow;
  plan?: ChangePlan;
  finding_targets?: FindingTarget[];
  /** The issues it fixes one at a time, each with its step's progress. */
  issues?: TaskIssue[];
  /** Linked issues a step found do not hold: merging the task dismisses them. */
  dismissed_targets?: FindingTarget[];
  /** Files outside the fix's folders its agents may change, and why. */
  scope_grants?: ScopeGrant[];
  /** Test files the testing agent wrote; a coding repair may not change them. */
  tester_tests?: string[];
  /** Tests the testing agent decided on after the coding agent disputed them. */
  decided_tests?: string[];
  /** Gaps the testing agent found the change cannot cover. */
  follow_ups?: FollowUp[];
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
  /** What a running workflow does now; absent when none runs. */
  workflow_phase?: WorkflowPhase;
  /** Where your review of the current version stands, when it waits for you. */
  review?: ReviewProgress;
  worktree_path?: string;
  finding_targets?: FindingTarget[];
}

/** The stage a running workflow is in (gitops.ChangeWorkflowPhase). */
export interface WorkflowPhase {
  stage: 'plan' | 'create' | 'test' | 'review';
  /** The issue being fixed (1-based) of `issues`, while a task fixes its issues one at a time. */
  issue?: number;
  issues?: number;
  /** The automatic repair the run is on; absent on its first pass. */
  repair?: number;
  /** A run started at the checks: its test stage only runs them. */
  checks_only?: boolean;
}

/** What your review of a version that waits for you needs next (gitops.ChangeReviewProgress). */
export interface ReviewProgress {
  step: 'view_files' | 'run_checks' | 'accept';
  files: number;
  viewed: number;
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

/** The files of a task in its own worktree that the main branch changed since the task's base commit. */
export interface MainChange {
  branch: string;
  head: string;
  paths: string[];
}

/** What retaking a task the main branch overtook decided: started again (still_valid) or deleted (not_valid). */
export interface ChangeRetake {
  outcome: 'still_valid' | 'not_valid';
  /** The model's explanation: a suggestion, not a verified finding. */
  reason: string;
  /** The task started again in its new worktree. */
  opened?: ChangeOpened;
  /** The old task, once deleted. */
  deleted?: string;
  dismissed_issues?: number;
  warnings: string[];
}

export interface ChangeMutationResult extends ProjectIdentity {
  session_id: string;
  state: string;
  hash: string;
  warnings: string[];
  commit?: ChangeCommit;
}

/** The explanation of one version of a proposal, written after its review; advice, never evidence. */
export interface ChangeWalkthrough {
  proposal_hash: string;
  status: 'ready' | 'failed';
  reason?: string;
  at: string;
  files?: WalkthroughFile[];
  /** The approved plan the notes were set against: a note without a plan_step is then outside that plan. */
  plan_hash?: string;
}

export interface WalkthroughFile {
  path: string;
  summary?: string;
  notes?: WalkthroughNote[];
}

/**
 * One hunk: where it starts in the proposed file, the goal it serves (the
 * 1-based issue, or criterion when the task has none; 0 = a change another
 * hunk needs), the wrong behavior it removes and, in a few words, how.
 */
export interface WalkthroughNote {
  line?: number;
  goal?: number;
  problem: string;
  how?: string;
  /** The hunk's part in the change's story. */
  kind?: 'cause' | 'fix' | 'support' | 'test' | string;
  /** The 1-based order a reviewer follows across all files; 0 or absent reads after the ordered hunks. */
  step?: number;
  /** Language idioms or patterns the hunk uses, for the learning log. */
  concepts?: string[];
  /** The step of the approved plan (from 1) the hunk carries out; none under a walkthrough with a plan_hash means outside the plan. */
  plan_step?: number;
}
