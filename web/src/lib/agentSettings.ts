// Agent role and budget editing: the model choices each role can take and the
// validation of a project's budget against the configured ceilings.

import type { AgentPreferences, AgentRole, AgentSettings, AvailableModels, Model, ModelsCurrent } from '@/api/types';
import { DEFAULT_ROLES } from './workflow';

export const ROLES: { role: AgentRole; label: string; hint: string }[] = [
  { role: 'creation', label: 'Writes the change', hint: 'Implements the task in an isolated copy of the project.' },
  { role: 'testing', label: 'Writes tests', hint: 'Adds or updates tests for the change, then runs the checks.' },
  { role: 'review', label: 'Reviews', hint: 'The specialist reviewers that check the change before you do.' },
];

export const LIMITS: { key: string; label: string; min: number }[] = [
  { key: 'model_calls', label: 'Model calls', min: 1 },
  { key: 'tool_calls', label: 'Tool calls', min: 1 },
  { key: 'input_tokens', label: 'Input tokens', min: 1 },
  { key: 'output_tokens', label: 'Output tokens', min: 1 },
  { key: 'elapsed_seconds', label: 'Time (seconds)', min: 1 },
  { key: 'concurrent_calls', label: 'Concurrent calls', min: 1 },
  { key: 'repairs', label: 'Repair rounds', min: 0 },
];

export interface ModelOption {
  id: string;
  label: string;
  model?: Model;
}

export interface ModelOptionGroup {
  label: string;
  options: ModelOption[];
}

/** Where a model runs, for a badge: local, or remote with its origin. */
export function modelLocation(model: Model | undefined): { remote: boolean; label: string } {
  if (!model) return { remote: false, label: 'unknown' };
  return model.remote_provider ? { remote: true, label: `remote · ${model.provider_origin || 'provider'}` } : { remote: false, label: 'local' };
}

/**
 * The choices for a role: the configured scopes first, then the models Claude
 * Code, Agy and Pi list. Configured catalog entries repeat the scopes, so they
 * are left out.
 * A current selection the lists do not hold stays selectable under its model.
 */
export function roleModelOptions(scopes: ModelsCurrent['scopes'], catalog: AvailableModels | undefined, current: string[], known: Partial<Record<string, Model>>): ModelOptionGroup[] {
  const configured = ['analyze', 'bug', 'code'].map(scope => {
    const model = scopes[scope];
    return { id: scope, label: model?.model ? `${scope} · ${model.model}` : scope, model };
  });
  const groups: ModelOptionGroup[] = [{ label: 'Configured in config.yaml', options: configured }];
  const sources = { claude: 'Claude Code models', agy: 'Agy models', pi: 'Pi models' } as const;
  for (const [source, label] of Object.entries(sources)) {
    const options = (catalog?.models ?? []).filter(choice => choice.source === source).map(choice => ({ id: choice.id, label: choice.name && choice.name !== choice.id ? `${choice.name} (${choice.id})` : choice.id, model: choice.model }));
    if (options.length) groups.push({ label, options });
  }
  const listed = new Set(groups.flatMap(group => group.options.map(option => option.id)));
  const extra = [...new Set(current)].filter(id => id && !listed.has(id)).map(id => ({ id, label: known[id]?.model ? `${known[id]!.model} (${id})` : id, model: known[id] }));
  if (extra.length) groups.push({ label: 'Current selection', options: extra });
  return groups;
}

/** The model a role's draft selection runs with, from the lists or the saved settings. */
export function selectedModel(groups: ModelOptionGroup[], id: string): Model | undefined {
  for (const group of groups) {
    const option = group.options.find(entry => entry.id === id);
    if (option) return option.model;
  }
  return undefined;
}

/** The editable values the settings hold now. */
export function preferencesOf(settings: AgentSettings): AgentPreferences {
  return {
    roles: {
      creation: settings.roles.creation ?? DEFAULT_ROLES.creation,
      testing: settings.roles.testing ?? DEFAULT_ROLES.testing,
      review: settings.roles.review ?? DEFAULT_ROLES.review,
    },
    limits: { ...(settings.limits ?? {}) },
  };
}

/** Per-limit errors: whole numbers within the minimum and the configured ceiling. */
export function limitErrors(limits: Record<string, number>, ceilings: Record<string, number> | undefined): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const { key, min } of LIMITS) {
    const value = limits[key];
    const ceiling = ceilings?.[key];
    if (!Number.isInteger(value) || value < min) errors[key] = `At least ${min}.`;
    else if (ceiling !== undefined && value > ceiling) errors[key] = `At most ${ceiling.toLocaleString()} (config.yaml).`;
  }
  return errors;
}

export function samePreferences(a: AgentPreferences, b: AgentPreferences): boolean {
  return ROLES.every(({ role }) => a.roles[role] === b.roles[role]) && LIMITS.every(({ key }) => a.limits[key] === b.limits[key]);
}
