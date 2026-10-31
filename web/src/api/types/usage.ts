// What agents spent: model and tool calls, tokens and estimated cost.

import type { ProjectIdentity } from './project';

/** Model and tool calls and tokens an agent budget recorded. */
export interface BudgetUsage {
  model_calls: number;
  tool_calls?: number;
  input_tokens: number;
  output_tokens: number;
  unknown_usage_calls?: number;
  elapsed_millis?: number;
}

/** A cost estimate from the configured price table; `total` only when every rate is known. */
export interface CostEstimate {
  status: string;
  currency?: string;
  price_version?: string;
  known_subtotal: number;
  total?: number;
  reasons: string[];
}

export interface UsageTotal {
  runs: number;
  usage: BudgetUsage;
  cost: CostEstimate;
}

export interface UsageRow extends UsageTotal {
  session_id: string;
  title: string;
  kind?: string;
  state?: string;
  updated_at: string;
}

/** Agent usage per task in a checkout, from the agent journal. */
export interface UsageReport extends ProjectIdentity {
  currency?: string;
  price_version?: string;
  tasks: UsageRow[];
  other: UsageTotal;
  total: UsageTotal;
}
