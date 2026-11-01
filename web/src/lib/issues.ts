// Pure helpers for the Issues page: grouping issues by file.

import type { Finding } from '@/api/types';
import { severityRank } from './util';

/** The most issues one task fixes together; the daemon refuses more. */
export const MAX_ISSUES_PER_TASK = 10;

export interface FileIssues {
  /** The file, or '' for issues about the whole project. */
  path: string;
  findings: Finding[];
}

/**
 * Groups issues by file: files with the most severe issue first, then those
 * with more issues; inside a file, most severe first, then by line. Issues
 * without a file come last.
 */
export function groupByFile(findings: Finding[]): FileIssues[] {
  const groups = new Map<string, Finding[]>();
  for (const finding of findings) {
    const path = finding.location?.path ?? '';
    groups.set(path, [...(groups.get(path) ?? []), finding]);
  }
  const worst = (list: Finding[]) => Math.min(...list.map(f => severityRank(f.severity)));
  return [...groups.entries()]
    .map(([path, list]) => ({ path, findings: [...list].sort((a, b) => severityRank(a.severity) - severityRank(b.severity) || (a.location?.start_line ?? 0) - (b.location?.start_line ?? 0)) }))
    .sort((a, b) => Number(!a.path) - Number(!b.path) || worst(a.findings) - worst(b.findings) || b.findings.length - a.findings.length || a.path.localeCompare(b.path));
}
