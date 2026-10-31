// Models and the agent roles that run with them.

import type { ProjectIdentity } from './project';

export interface ModelCapabilities {
  completion?: boolean;
  json_schema?: boolean;
  tools?: boolean;
  streaming?: boolean;
}

export interface Model {
  scope: string;
  profile?: string;
  model: string;
  provider_origin?: string;
  remote_provider?: boolean;
  reasoning_effort?: string;
  timeout?: string;
  context_max_tokens?: number;
  capabilities?: ModelCapabilities;
}

export interface ModelsCurrent {
  scopes: Record<string, Model>;
  /** The configuration file the daemon read; empty when it uses the built-in local defaults. */
  config_file?: string;
}

export type AgentRole = 'creation' | 'testing' | 'review';

export interface AgentSettings extends ProjectIdentity {
  configuration?: string;
  version?: number;
  state: 'configured' | 'overridden' | 'stale' | string;
  roles: Record<string, string>;
  limits?: Record<string, number>;
  ceilings?: Record<string, number>;
  skills?: { directory: string; enabled: boolean }[];
  models?: Model[];
  /** The model each role runs with; a role whose model no longer resolves has no entry. */
  role_models?: Partial<Record<AgentRole, Model>>;
}

/** A complete replacement of the project's agent settings. */
export interface AgentPreferences {
  roles: Record<AgentRole, string>;
  limits: Record<string, number>;
  disabled_skills: string[];
}

/** A model the daemon can assign: a configured scope's model or one an installed CLI lists. */
export interface ModelChoice {
  id: string;
  name: string;
  provider: string;
  source: 'configured' | 'agy' | 'pi' | 'claude' | string;
  location: 'local' | 'remote' | 'unknown' | string;
  model: Model;
}

export interface ModelDiscovery {
  status: 'ready' | 'unavailable' | string;
  message?: string;
}

export interface AvailableModels {
  models: ModelChoice[];
  defaults: Record<string, string>;
  pi: ModelDiscovery;
  agy: ModelDiscovery;
  /** Claude Code: ready when it is installed; its choices are model aliases. */
  claude: ModelDiscovery;
}
