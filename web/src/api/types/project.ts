// The project: its identity, indexed files, execution trust and overview.

import type { AnalysisRunSummary } from './analysis';
import type { DocsRefresh, ProjectDocs } from './docs';

export interface ProjectIdentity {
  project_id: string;
  project_revision: string;
}

export interface GitIdentity {
  head: string;
  branch: string;
}

export interface Project extends ProjectIdentity {
  name: string;
  path: string;
  type: string;
  build_file?: string;
  file_count: number;
  source_file_count?: number;
  total_lines?: number;
  languages?: Record<string, number>;
  summary?: string;
}

export interface IndexedFile {
  path: string;
  content_hash: string;
  language: string;
  binary?: boolean;
  size_bytes?: number;
  line_count?: number;
  analysis_status?: string;
}

export interface ProjectIndex extends ProjectIdentity {
  files: IndexedFile[];
}

export interface FileInfo {
  path: string;
  content_hash: string;
  name?: string;
  language: string;
  size_bytes?: number;
  line_count?: number;
  binary?: boolean;
  content: string;
}

export interface ExecutionTrust extends ProjectIdentity {
  trusted: boolean;
  commands: string[][];
}

export interface Overview extends ProjectIdentity {
  docs?: ProjectDocs;
  /** The latest refresh started from the Docs page. */
  docs_refresh?: DocsRefresh;
  metrics?: { type?: string; file_count?: number; source_file_count?: number; total_lines?: number; languages?: Record<string, number> };
  analysis?: { status?: string; purpose?: string; failure?: string; model?: string; generated_at?: string };
  analysis_coverage?: Record<string, number>;
  analysis_run?: AnalysisRunSummary;
  finding_counts?: { verified?: number; ai_suggestions?: number };
}
