// Analysis runs: their plan, each file's stages, sections and project checks.

import type { DocsRefresh } from './docs';
import type { Model } from './models';

export interface AnalysisRunIdentity {
  project_id: string;
  project_revision: string;
  policy_fingerprint: string;
  provider_fingerprint: string;
  queue_id: string;
  id: string;
  generation: string;
}

export type AnalysisStageName = 'semantic' | 'performance' | 'security_rules' | 'security_ai';

export interface AnalysisStageProgress {
  stage: AnalysisStageName | string;
  status: string;
  attempts: number;
  cached: boolean;
  finding_count?: number | null;
  reason?: string;
}

export interface AnalysisRunFile {
  path: string;
  language?: string;
  stages: AnalysisStageProgress[];
}

export interface AnalysisCoverageCounts {
  total: number;
  pending: number;
  running: number;
  succeeded: number;
  partial: number;
  failed: number;
  skipped: number;
  unavailable: number;
}

export interface AnalysisSection {
  category: 'bugs' | 'performance' | 'security' | string;
  status: string;
  coverage: AnalysisCoverageCounts;
  finding_count?: number | null;
}

export interface AnalysisProvider {
  id: string;
  stages: string[];
  model: Model;
  remote_confirmation_required?: boolean;
}

/** An analysis run: what it planned, each file's stages and the totals per section. */
export interface AnalysisRunSummary {
  identity?: AnalysisRunIdentity;
  status: string;
  reason?: string;
  files?: AnalysisRunFile[];
  sections?: AnalysisSection[];
  plan?: {
    limits?: { batch_files: number; budget_seconds: number; max_attempts_per_stage: number };
    /** Retained files are outside a scoped run (such as a retry): their saved results are kept, not analyzed again. */
    files?: { path: string; retained?: boolean }[];
    providers?: AnalysisProvider[];
    excluded?: { path: string; reason: string }[];
    refresh?: boolean;
    retry_stale_failed?: boolean;
    expected_model_requests?: number;
    max_model_requests?: number;
    checks?: AnalysisChecksPlan;
  };
  /** The project checks that ran after the files. */
  checks?: AnalysisChecks | null;
  /** The docs refresh that ended the run. */
  docs?: DocsRefresh | null;
  elapsed_seconds?: number;
  window_files_completed?: number;
  created_at?: string;
  started_at?: string;
  updated_at?: string;
}

/** One command of a run's project checks, run in its subproject; never through a shell. */
export interface AnalysisCheckCommand {
  name: string;
  kind: 'lint' | 'test' | 'semgrep' | string;
  dir?: string;
  command: string[];
}

/** The project checks an analysis runs after its files. */
export interface AnalysisChecksPlan {
  commands: AnalysisCheckCommand[];
  /** Why commands that run the project's code are left out. */
  reason?: string;
}

/** How one check went: parse and code budgets have no command. */
export interface AnalysisCheckResult {
  name: string;
  command?: string[];
  state: 'passed' | 'failed' | 'skipped' | 'canceled' | 'unavailable' | string;
  issues: number;
  output?: string;
  duration_ms?: number;
}

/** What a run's checks did; failures are issues on the Issues page. */
export interface AnalysisChecks {
  status: 'completed' | 'canceled' | 'failed' | string;
  checks: AnalysisCheckResult[];
  issues: number;
  reason?: string;
  at: string;
}
