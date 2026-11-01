// An agent run on a task: its stages, reviews, override, input and pause.

import type { Model } from './models';
import type { BudgetUsage, CostEstimate } from './usage';

export interface WorkflowStage {
  name: string;
  status: string;
  reason?: string;
  started_at?: string;
  finished_at?: string;
  model?: Model;
  /** What the stage spent across attempts, and its estimated cost. */
  usage?: BudgetUsage;
  cost?: CostEstimate;
  /** The stage's latest run as a tool-using agent; absent for a one-shot proposal. */
  tools?: StageTools;
}

/** What a tool-using agent did in a stage. */
export interface StageTools {
  calls: number;
  /** Calls per tool, most used first: "read_file 12, edit_file 4". */
  summary?: string;
  turns: number;
  finished: boolean;
}

/** A reviewer's finding or note: a few words, where it is, and why. */
export interface ReviewItem {
  title: string;
  /** The task file it is about. */
  path?: string;
  /** The line in the file's proposed content; absent when the reviewer's quote was not found. */
  line?: number;
  detail?: string;
}

export interface WorkflowReview {
  proposal_hash?: string;
  verdict: 'approve' | 'changes_requested' | string;
  summary: string;
  /** Blocking issues: a review approves exactly when there are none. */
  findings: ReviewItem[];
  /** Advisory suggestions that never decide the verdict. */
  notes?: ReviewItem[];
}

/** A human override of the reviewers' rejection, bound to one proposal hash. */
export interface WorkflowOverride {
  proposal_hash: string;
  reason: string;
  at: string;
}

export interface SpecialistReview {
  id: string;
  status: string;
  reason?: string;
  evidence: string;
  model?: Model;
  review?: WorkflowReview;
}

export interface WorkflowInput {
  request_id: string;
  workflow_id: string;
  status: string;
  action: string;
  expires_at: string;
}

export interface ChangeWorkflow {
  id: string;
  status: string;
  reason?: string;
  attempt?: number;
  /** Where this run started: create (the default), test, checks or review. */
  resume_from?: string;
  auto_repair?: boolean;
  auto_repair_attempts?: number;
  stages: WorkflowStage[];
  review?: WorkflowReview;
  override?: WorkflowOverride;
  specialists?: SpecialistReview[];
  input?: WorkflowInput;
  /** Set while the run is paused because a stage's subscription plan reached its usage limit. */
  pause?: WorkflowPause;
  started_at?: string;
  updated_at?: string;
}

/** Where a run stopped at a plan's usage limit, and how to continue it. */
export interface WorkflowPause {
  /** The stage that reached the limit: create, test or review. */
  stage: string;
  /** The resume_from that continues the run: create, test, checks or review. */
  resume_from: string;
  /** The CLI whose plan reached its limit: claude, agy or pi. */
  provider: string;
  /** When the plan's limit resets, when the CLI said so. */
  resumes_at?: string;
}
