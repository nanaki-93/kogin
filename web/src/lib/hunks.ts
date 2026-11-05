// Hunk-by-hunk review: a file's diff split into the hunks a reviewer reads
// one at a time, and the hunks the reviewer already called right.

import type { DiffLine, FileChange } from '@/api/types';
import { buildSegments, isChange, lineAnchor, type LineAnchor } from './diff';

export interface Hunk {
  path: string;
  /** Its place among the file's hunks, from 0. */
  n: number;
  /** The slice of the file's diff lines it covers, with context. */
  start: number;
  end: number;
}

/** The hunks of a file's diff: each run of changes with three lines of context, as the full diff shows them. */
export function fileHunks(path: string, lines: DiffLine[]): Hunk[] {
  return buildSegments(lines, 3)
    .filter(segment => segment.type === 'lines' && lines.slice(segment.start, segment.end).some(isChange))
    .map((segment, n) => ({ path, n, start: segment.start, end: segment.end }));
}

/** Every hunk of the edits, file by file. */
export function reviewHunks(edits: FileChange[]): Hunk[] {
  return edits.flatMap(edit => fileHunks(edit.path, edit.diff?.lines ?? []));
}

/** Where a comment on the hunk goes: its first changed line. */
export function hunkAnchor(lines: DiffLine[], hunk: Hunk): LineAnchor | null {
  for (let index = hunk.start; index < hunk.end; index++) {
    if (isChange(lines[index])) return lineAnchor(lines[index]);
  }
  return null;
}

/** Whether the hunk shows `line` of the new file. */
export function hunkHasLine(lines: DiffLine[], hunk: Hunk, line: number): boolean {
  return lines.slice(hunk.start, hunk.end).some(entry => entry.kind !== 'removed' && entry.new_line === line);
}

export const hunkKey = (hunk: Pick<Hunk, 'path' | 'n'>) => `${hunk.path}#${hunk.n}`;

/**
 * The hunks the reviewer called right, per task version, kept in this
 * browser: a new version starts over, like the files viewed.
 */
export function loadVerdicts(id: string, hash: string): Set<string> {
  try {
    const raw = localStorage.getItem(verdictKey(id, hash));
    const list: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(list) ? list.filter((item): item is string => typeof item === 'string') : []);
  } catch {
    return new Set();
  }
}

export function saveVerdicts(id: string, hash: string, verdicts: Set<string>): void {
  try {
    localStorage.setItem(verdictKey(id, hash), JSON.stringify([...verdicts]));
  } catch {
    // A browser without storage keeps the verdicts for this visit only.
  }
}

const verdictKey = (id: string, hash: string) => `kogin.hunks.${id}.${hash}`;
