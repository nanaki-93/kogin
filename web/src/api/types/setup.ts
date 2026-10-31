// Project setup: the configuration file's models, roles, limits and time
// limits, and which files analysis leaves out.

import type { AgentRole } from './models';
import type { ProjectIdentity } from './project';

export type SetupProvider = '' | 'openai' | 'gemini' | 'vertex' | 'agy' | 'pi' | 'claude';

/** One model scope as setup edits it. Secrets are write-only: api_key_set says one is saved. */
export interface SetupModel {
  provider: SetupProvider | string;
  cli_path?: string;
  api_base_url?: string;
  api_key?: string;
  api_key_set?: boolean;
  clear_api_key?: boolean;
  vertex_project?: string;
  vertex_location?: string;
  model: string;
  reasoning_effort?: string;
  temperature?: number;
  max_tokens?: number;
  context_max_tokens?: number;
}

export type SetupScope = 'analyze' | 'bug' | 'code';

export interface ProjectSetup extends ProjectIdentity {
  state: 'needed' | 'skipped' | 'configured' | 'invalid' | string;
  config_file: string;
  daemon_config_file: string;
  error?: string;
  models: Record<SetupScope, SetupModel>;
  roles: Record<AgentRole, string>;
  limits: Record<string, number>;
  /** The time limits in effect, in seconds, defaults included. */
  timeouts: SetupTimeouts;
  /** Saving replaced agent overrides from Settings → Models & agents. */
  overrides_reset?: boolean;
}

export interface SetupTimeouts {
  import_seconds: number;
  analysis_seconds: number;
  generation_seconds: number;
  workflow_model_seconds: number;
  focused_check_seconds: number;
}

export interface SetupModelTest {
  ok: boolean;
  model?: string;
  latency_ms: number;
  reason?: string;
  detail?: string;
}

export interface AnalysisFileSelection extends ProjectIdentity {
  selection_id: string;
  excluded_paths: string[];
  files: { path: string; reason: string }[];
  editable: boolean;
}
