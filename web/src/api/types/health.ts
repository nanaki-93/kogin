// The project profile and health (GET/POST /api/projects/current/profile,
// GET /api/projects/current/health, POST /api/projects/current/health/tests).

import type { IssueTaskDraft } from './issues';

export type ProjectKind = 'personal' | 'team' | 'open_source' | 'enterprise';
export type ProjectShape = 'cli' | 'gui_backend' | 'web_service' | 'library' | 'other';
export type ProjectPlatform = 'macos' | 'linux' | 'windows';
export type SecurityLevel = 'basic' | 'standard' | 'strict';
export type FinishSeverity = 'critical' | 'high' | 'medium' | 'low';

/** What a project is and what "done" means for it. */
export interface ProjectProfile {
  kind: ProjectKind;
  purpose: string;
  shape: ProjectShape;
  /** Empty: every platform. */
  platforms: ProjectPlatform[];
  security: SecurityLevel;
  /** The lowest severity that still counts as work. */
  finish_line: FinishSeverity;
  analyze_tests: boolean;
  /** False while the project uses the default profile. */
  saved: boolean;
}

export interface HealthFinishLine {
  severity: FinishSeverity;
  open: number;
  by_severity: Record<string, number>;
  out_of_scope: Record<string, number>;
  /** Null until tests ran on this revision. */
  tests_pass: boolean | null;
  done: boolean;
}

export interface HealthTestDir {
  dir: string;
  source_files: number;
  test_files: number;
  test_lines: number;
}

export interface HealthCode {
  source_files: number;
  source_lines: number;
  test_files: number;
  test_lines: number;
  test_dirs: HealthTestDir[];
}

export interface HealthPackage {
  name: string;
  passed: boolean;
  no_tests?: boolean;
  coverage: number | null;
}

export interface HealthTestCommand {
  dir: string;
  command: string[];
  state: string;
  output?: string;
  duration_ms: number;
}

export interface HealthTestRun {
  revision: string;
  current: boolean;
  ran_at: string;
  duration_ms: number;
  passed: boolean;
  coverage: number | null;
  packages: HealthPackage[];
  commands: HealthTestCommand[];
}

export interface HealthPlace {
  path: string;
  start_line: number;
  end_line: number;
}

export interface HealthDuplicateGroup {
  lines: number;
  places: HealthPlace[];
  preview: string;
}

export interface HealthDuplication {
  repeated_lines: number;
  total_lines: number;
  groups: HealthDuplicateGroup[];
  test_repeated_lines: number;
  test_total_lines: number;
}

export interface HealthUnusedFunction {
  path: string;
  function: string;
  start_line: number;
  test_only?: boolean;
}

export interface HealthComplexity {
  path: string;
  function: string;
  start_line: number;
  complexity: number;
  lines: number;
  budget: number;
  line_budget?: number;
}

export interface HealthDay {
  date: string;
  found: number;
  fixed: number;
}

export interface HealthTrend {
  days: HealthDay[];
  found_7: number;
  fixed_7: number;
  verdict: 'converging' | 'growing' | 'steady' | 'quiet';
}

export interface HealthTask {
  id: string;
  kind: 'unused' | 'duplication' | 'tests' | 'complexity' | 'coverage';
  title: string;
  why: string;
  draft: IssueTaskDraft;
}

export interface ProjectHealth {
  profile: ProjectProfile;
  finish_line: HealthFinishLine;
  code: HealthCode;
  tests: HealthTestRun | null;
  tests_running: boolean;
  unused: HealthUnusedFunction[];
  duplication: HealthDuplication;
  complexity: HealthComplexity[];
  trend: HealthTrend;
  tasks: HealthTask[];
}
