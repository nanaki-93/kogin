// The handbook (docs), its refresh and answers about the code.

import type { ProjectIdentity } from './project';

/** The summary an analysis run cached for one file; reading it never calls a model. */
export interface FileSummary {
  path: string;
  status: 'fresh' | 'stale' | 'missing' | 'failed' | 'running' | string;
  purpose?: string;
  responsibilities?: string[];
  dependencies?: string[];
  side_effects?: string[];
  generated_at?: string;
}

/** One part of the project: a directory of code, as the project map groups it. */
export interface DocsComponent {
  name: string;
  layer?: string;
  path: string;
  languages?: string[];
  files?: number;
  lines?: number;
  /** Model-written; summary_source says whether from the module's own summary or the project overview. */
  summary?: string;
  summary_source?: 'module' | 'overview' | string;
  responsibilities?: string[];
  /** The component's entry points. */
  contracts?: { name: string; kind: string; summary?: string }[];
  /** Components this one imports, and those that import it, resolved from the code. */
  dependencies?: string[];
  used_by?: string[];
}

/** What the components diagram is drawn from. */
export interface DocsGraph {
  edges: { from: string; to: string; imports: number }[];
  entry_points: { path: string; kind: string; detail?: string }[];
  modules: number;
  truncated?: boolean;
}

export interface ProjectDocs {
  status: string;
  context?: { purpose?: string; goal_pursuit?: string[]; topology?: string; stack?: string[] };
  dependencies?: { name: string; kind?: string; version?: string; source?: string; summary?: string }[];
  components?: DocsComponent[];
  scenarios?: { title: string; trigger?: string; summary?: string; invariant?: string; steps?: { phase?: string; layer?: string; title: string; detail?: string; path?: string }[] }[];
  decisions?: { title: string; category?: string; decision?: string; tradeoff?: string }[];
  runbook?: { label: string; command: string; description?: string }[];
  glossary?: { term: string; definition: string }[];
  graph?: DocsGraph;
  updated_at?: string;
}

/** Lines of a file that support an answer; the daemon found the quote in them. */
export interface AskCitation {
  path: string;
  start_line: number;
  end_line: number;
  quote: string;
  content_hash: string;
}

/** The answer to a question about the code; it is not kept. */
export interface AskAnswer extends ProjectIdentity {
  question: string;
  answer: string;
  citations: AskCitation[];
  model: string;
  turns: number;
  tool_calls: number;
  at: string;
}

/** A refresh of the docs' model-written parts: component summaries and the overview. */
export interface DocsRefresh {
  status: 'running' | 'completed' | 'skipped' | 'failed' | string;
  components: number;
  summarized: number;
  kept: number;
  overview: 'written' | 'kept' | 'failed' | 'skipped' | string;
  reason?: string;
  at: string;
}
