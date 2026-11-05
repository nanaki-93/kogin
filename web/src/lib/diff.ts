// Diff shaping for review. The daemon sends every line of a changed file;
// review shows changed lines with context and lets the reviewer expand gaps.

import type { DiffLine, FileChange } from '@/api/types';

export function isChange(line: DiffLine | undefined | null): boolean {
  return Boolean(line && (line.kind === 'added' || line.kind === 'removed'));
}

export function diffStats(edit: Pick<FileChange, 'diff'> | { lines: DiffLine[] }): { added: number; removed: number } {
  const lines = 'diff' in edit ? edit.diff?.lines ?? [] : edit.lines;
  let added = 0;
  let removed = 0;
  for (const line of lines) {
    if (line.kind === 'added') added++;
    else if (line.kind === 'removed') removed++;
  }
  return { added, removed };
}

export type Segment = { type: 'lines'; start: number; end: number } | { type: 'gap'; start: number; end: number; count: number };

/**
 * Groups lines into visible hunks and collapsed gaps; `expanded` holds gap
 * start indexes and `pinned` line indexes that stay visible (commented lines).
 */
export function buildSegments(lines: DiffLine[], context = 3, expanded: ReadonlySet<number> = new Set(), pinned: ReadonlySet<number> = new Set()): Segment[] {
  if (!lines.length) return [];
  const keep = new Array<boolean>(lines.length).fill(false);
  let anyChange = false;
  lines.forEach((line, index) => {
    if (!isChange(line)) return;
    anyChange = true;
    for (let k = Math.max(0, index - context); k <= Math.min(lines.length - 1, index + context); k++) keep[k] = true;
  });
  for (const index of pinned) {
    if (index >= 0 && index < lines.length) [keep[index], anyChange] = [true, true];
  }
  if (!anyChange) return [{ type: 'gap', start: 0, end: lines.length, count: lines.length }];
  const segments: Segment[] = [];
  let index = 0;
  while (index < lines.length) {
    const visible = keep[index];
    let end = index;
    while (end < lines.length && keep[end] === visible) end++;
    if (visible || expanded.has(index) || end - index <= 2) segments.push({ type: 'lines', start: index, end });
    else segments.push({ type: 'gap', start: index, end, count: end - index });
    index = end;
  }
  const merged: Segment[] = [];
  for (const segment of segments) {
    const last = merged[merged.length - 1];
    if (last && last.type === 'lines' && segment.type === 'lines') last.end = segment.end;
    else merged.push({ ...segment });
  }
  return merged;
}

/** Indexes where runs of changed lines begin, for hunk navigation. */
export function hunkStarts(lines: DiffLine[]): number[] {
  const starts: number[] = [];
  lines.forEach((line, index) => {
    if (isChange(line) && !isChange(lines[index - 1])) starts.push(index);
  });
  return starts;
}

/**
 * Where a note on a hunk goes: the index of its line (a line of the new file)
 * moved back to the start of the run of changes it is in, so a replaced
 * line's removed half stays under its note. -1 when the diff lacks the line.
 */
export function noteStart(lines: DiffLine[], line: number): number {
  const index = lines.findIndex(entry => (entry.kind === 'added' || entry.kind === 'context') && entry.new_line === line);
  if (index < 0) return -1;
  let start = index;
  while (start > 0 && isChange(lines[start]) && isChange(lines[start - 1])) start--;
  return start;
}

/**
 * The row a hunk note heads: its start moved up over at most `context`
 * unchanged lines that lead into it, staying in its visible segment and
 * below the change before it.
 */
export function noteHead(lines: DiffLine[], segments: Segment[], start: number, context = 3): number {
  const segment = segments.find(entry => entry.type === 'lines' && start >= entry.start && start < entry.end);
  const top = Math.max(segment?.start ?? start, start - context);
  let head = start;
  while (head > top && !isChange(lines[head - 1]) && lines[head - 1]?.kind !== 'hunk') head--;
  return head;
}

export interface SplitRow {
  left: DiffLine | null;
  right: DiffLine | null;
  index: number;
}

/** Pairs removed/added runs side by side for split view. */
export function splitRows(lines: DiffLine[], start = 0, end = lines.length): SplitRow[] {
  const rows: SplitRow[] = [];
  let i = start;
  while (i < end) {
    const line = lines[i];
    if (!isChange(line)) {
      rows.push({ left: line, right: line, index: i });
      i++;
      continue;
    }
    const first = i;
    const removed: DiffLine[] = [];
    const added: DiffLine[] = [];
    while (i < end && lines[i].kind === 'removed') removed.push(lines[i++]);
    while (i < end && lines[i].kind === 'added') added.push(lines[i++]);
    for (let k = 0; k < Math.max(removed.length, added.length); k++) rows.push({ left: removed[k] ?? null, right: added[k] ?? null, index: first + k });
  }
  return rows;
}

export interface ParsedFileDiff {
  path: string;
  lines: DiffLine[];
  binary: boolean;
}

/** Parses `git diff` text (merge previews) into per-file line lists. */
export function parseGitDiff(text: string | undefined): ParsedFileDiff[] {
  const files: ParsedFileDiff[] = [];
  let current: ParsedFileDiff | null = null;
  let oldLine = 0;
  let newLine = 0;
  for (const raw of (text ?? '').split('\n')) {
    if (raw.startsWith('diff --git ')) {
      const match = /^diff --git a\/(.+?) b\/(.+)$/.exec(raw);
      current = { path: match ? match[2] : raw.slice(11), lines: [], binary: false };
      files.push(current);
      continue;
    }
    if (!current) continue;
    if (/^(\+\+\+ |--- |index |new file|deleted file|similarity|rename )/.test(raw)) continue;
    if (raw.startsWith('Binary files')) {
      current.binary = true;
      continue;
    }
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(raw);
    if (hunk) {
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
      current.lines.push({ kind: 'hunk', text: raw });
      continue;
    }
    if (raw.startsWith('+')) current.lines.push({ kind: 'added', new_line: newLine++, text: raw.slice(1) });
    else if (raw.startsWith('-')) current.lines.push({ kind: 'removed', old_line: oldLine++, text: raw.slice(1) });
    else if (raw.startsWith(' ')) current.lines.push({ kind: 'context', old_line: oldLine++, new_line: newLine++, text: raw.slice(1) });
  }
  return files;
}

/** Where a line comment attaches: removed lines on the old side, others on the new. */
export interface LineAnchor {
  side: 'new' | 'old';
  line: number;
}

export function lineAnchor(line: DiffLine | null | undefined): LineAnchor | null {
  if (!line || line.kind === 'hunk') return null;
  if (line.kind === 'removed') return line.old_line ? { side: 'old', line: line.old_line } : null;
  return line.new_line ? { side: 'new', line: line.new_line } : null;
}

export const anchorKey = (anchor: LineAnchor) => `${anchor.side}:${anchor.line}`;
