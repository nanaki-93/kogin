// The code rules AGENTS.md files declare and a task's files measured against them.

/** An AGENTS.md whose rules block could not be read. */
export interface RulesProblem {
  path: string;
  message: string;
}

/** GET /rules: how the daemon tells test files apart, for the Web UI to match. */
export interface ProjectRules {
  project_id: string;
  project_revision: string;
  test_patterns: string[];
  guides: string[];
  problems: RulesProblem[];
}

export interface CodeBudgets {
  file_lines?: number;
  function_lines?: number;
  function_complexity?: number;
}

export interface BudgetFinding {
  kind: 'file_lines' | 'function_lines' | 'function_complexity';
  function?: string;
  line?: number;
  value: number;
  /** The value before the task; absent for a new file or function. */
  base?: number;
  limit: number;
}

/** One task file measured against its budgets and import rules. */
export interface ChangeRulesFile {
  path: string;
  budgets: CodeBudgets;
  lines: number;
  base_lines?: number;
  not_checked?: string;
  over?: BudgetFinding[];
  debt?: BudgetFinding[];
  test: boolean;
  proposed: boolean;
  imports?: { from: string; never: string[] }[];
  sources?: string[];
}

export interface ChangeRules {
  session_id: string;
  revision: number;
  files: ChangeRulesFile[];
  problems: RulesProblem[];
}
