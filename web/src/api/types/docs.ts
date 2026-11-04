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

/** Where a program starts: a main function, a package's bin or main, a script. */
export interface DocsEntryPoint {
  path: string;
  kind: string;
  detail?: string;
}

/** One part of the project: a directory of code, as the project map groups it. */
export interface DocsComponent {
  name: string;
  path: string;
  languages?: string[];
  files?: number;
  lines?: number;
  /** Model-written; summary_source says whether from the module's own summary or the project overview. */
  summary?: string;
  summary_source?: 'module' | 'overview' | string;
  responsibilities?: string[];
  entry_points?: DocsEntryPoint[];
  /** Components this one imports, and those that import it, resolved from the code. */
  imports?: string[];
  used_by?: string[];
}

/** What the components diagram is drawn from. */
export interface DocsGraph {
  edges: { from: string; to: string; imports: number }[];
  entry_points: DocsEntryPoint[];
  modules: number;
  truncated?: boolean;
}

/** One step of a walkthrough; symbol and line only when the daemon found the declaration. */
export interface WalkthroughStep {
  text: string;
  path: string;
  symbol?: string;
  line?: number;
}

/** One thing the project does, traced through its files by the overview's model. */
export interface Walkthrough {
  title: string;
  summary: string;
  steps: WalkthroughStep[];
}

/** A command the project declares: a subproject's test or lint command, or a package script. */
export interface DocsCommand {
  label: string;
  command: string;
  /** Where it runs, relative to the project root; absent for the root. */
  dir?: string;
  source: string;
}

export interface DocsDependency {
  name: string;
  kind: string;
  version?: string;
  dev?: boolean;
  source: string;
}

export interface ProjectDocs {
  schema_version?: string;
  /** The model-written parts' freshness: the project overview's status. */
  status: string;
  overview?: { purpose?: string; architecture?: string; languages?: { name: string; files: number }[]; files?: number; lines?: number };
  components?: DocsComponent[];
  walkthroughs?: Walkthrough[];
  /** The changes a developer most likely makes, each as the files and functions to touch in order; absent before overview v8. */
  change_guide?: Walkthrough[];
  commands?: DocsCommand[];
  dependencies?: DocsDependency[];
  graph?: DocsGraph;
  updated_at?: string;
}

/** A docs component's code files, each with the first sentence of its saved summary. */
export interface DocsComponentFiles {
  path: string;
  files: { path: string; language: string; lines: number; summary?: string }[];
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
