// Hunk-by-hunk review: a file's diff split into the hunks a reviewer reads
// one at a time, and the hunks the reviewer already called right. When the
// walkthrough ordered the hunks (cause → fix → support → test), the guided
// order replaces file order.

import type { ChangeWalkthrough, DiffLine, FileChange } from '@/api/types';
import { buildSegments, isChange, lineAnchor, noteStart, type LineAnchor } from './diff';

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

/** The hunks in the walkthrough's guided order, when it ordered them. */
export interface GuidedHunks {
  hunks: Hunk[];
  /** Whether the walkthrough's causal order replaced file order. */
  guided: boolean;
  /** Each hunk's part in the story (cause, fix, support or test), by hunk key. */
  kinds: Map<string, string>;
}

/**
 * Orders the hunks the way the walkthrough tells the change's story: the
 * problem's cause and fix first, then supporting changes, then the tests.
 * Hunks its notes did not order keep their file order after the ordered
 * ones. Without a current walkthrough with steps, file order stands.
 */
export function guidedHunks(session: { walkthrough?: ChangeWalkthrough; hash: string }, linesOf: Map<string, DiffLine[]>, all: Hunk[]): GuidedHunks {
  const walkthrough = session.walkthrough;
  const kinds = new Map<string, string>();
  if (walkthrough?.status !== 'ready' || walkthrough.proposal_hash !== session.hash) return { hunks: all, guided: false, kinds };
  const steps = new Map<string, number>();
  for (const file of walkthrough.files ?? []) {
    const lines = linesOf.get(file.path) ?? [];
    const hunks = all.filter(hunk => hunk.path === file.path);
    for (const note of file.notes ?? []) {
      const at = note.line ? noteStart(lines, note.line) : -1;
      const hunk = at >= 0 ? hunks.find(entry => at >= entry.start && at < entry.end) : undefined;
      if (!hunk) continue;
      const key = hunkKey(hunk);
      if (note.kind && !kinds.has(key)) kinds.set(key, note.kind);
      if (note.step && note.step < (steps.get(key) ?? Infinity)) steps.set(key, note.step);
    }
  }
  if (!steps.size) return { hunks: all, guided: false, kinds };
  const hunks = [...all].sort((a, b) => (steps.get(hunkKey(a)) ?? Infinity) - (steps.get(hunkKey(b)) ?? Infinity));
  return { hunks, guided: true, kinds };
}

/** How a hunk's part in the story reads on its chip. */
export const HUNK_KINDS: Record<string, { label: string; tone: 'danger' | 'success' | 'neutral' | 'info' }> = {
  cause: { label: 'Cause', tone: 'danger' },
  fix: { label: 'Fix', tone: 'success' },
  support: { label: 'Supports the fix', tone: 'neutral' },
  test: { label: 'Proof', tone: 'info' },
};

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
