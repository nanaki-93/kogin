import { describe, expect, it } from 'vitest';
import type { DiffLine } from '@/api/types';
import { anchorKey, buildSegments, diffStats, hunkStarts, lineAnchor, noteHead, noteStart, parseGitDiff, splitRows } from './diff';

const ctx = (n: number): DiffLine => ({ kind: 'context', old_line: n, new_line: n, text: 'line ' + n });

describe('diff shaping', () => {
  const lines: DiffLine[] = [...Array.from({ length: 10 }, (_, i) => ctx(i + 1)), { kind: 'removed', old_line: 11, text: 'old' }, { kind: 'added', new_line: 11, text: 'new' }, ...Array.from({ length: 10 }, (_, i) => ctx(i + 12))];

  it('keeps context around changes and collapses the rest', () => {
    expect(buildSegments(lines, 3)).toEqual([
      { type: 'gap', start: 0, end: 7, count: 7 },
      { type: 'lines', start: 7, end: 15 },
      { type: 'gap', start: 15, end: 22, count: 7 },
    ]);
  });

  it('keeps commented lines visible inside collapsed context', () => {
    expect(buildSegments(lines, 3, new Set(), new Set([1]))).toEqual([
      { type: 'lines', start: 0, end: 2 },
      { type: 'gap', start: 2, end: 7, count: 5 },
      { type: 'lines', start: 7, end: 15 },
      { type: 'gap', start: 15, end: 22, count: 7 },
    ]);
  });

  it('puts a hunk note where its run of changes starts', () => {
    // The added line 11 replaces old 11: the note goes above the removed half.
    expect(noteStart(lines, 11)).toBe(10);
    // A context line (the line kept beside a removal) holds its own note.
    expect(noteStart(lines, 4)).toBe(3);
    // A line the diff does not have has no place.
    expect(noteStart(lines, 99)).toBe(-1);
  });

  it('heads a hunk with its note, above the context that leads into it', () => {
    const segments = buildSegments(lines, 3);
    expect(noteHead(lines, segments, 10)).toBe(7);
    // Never into collapsed lines, nor above the change before it.
    expect(noteHead(lines, [{ type: 'lines', start: 9, end: 15 }], 10)).toBe(9);
    expect(noteHead(lines, segments, 13)).toBe(12);
  });

  it('anchors comments to the old side of removed lines and the new side otherwise', () => {
    expect(lineAnchor(lines[10])).toEqual({ side: 'old', line: 11 });
    expect(lineAnchor(lines[11])).toEqual({ side: 'new', line: 11 });
    expect(anchorKey(lineAnchor(ctx(4))!)).toBe('new:4');
    expect(lineAnchor({ kind: 'hunk', text: '@@' })).toBeNull();
  });

  it('expands a gap when asked and merges adjacent visible runs', () => {
    expect(buildSegments(lines, 3, new Set([0]))).toEqual([
      { type: 'lines', start: 0, end: 15 },
      { type: 'gap', start: 15, end: 22, count: 7 },
    ]);
  });

  it('collapses an unchanged file into one gap', () => {
    expect(buildSegments([ctx(1), ctx(2)])).toEqual([{ type: 'gap', start: 0, end: 2, count: 2 }]);
  });

  it('counts stats, hunks and pairs split rows', () => {
    expect(diffStats({ lines })).toEqual({ added: 1, removed: 1 });
    expect(hunkStarts(lines)).toEqual([10]);
    const rows = splitRows(lines, 9, 13);
    expect(rows.map(r => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([
      ['line 10', 'line 10'],
      ['old', 'new'],
      ['line 12', 'line 12'],
    ]);
  });

  it('pads uneven replacement runs', () => {
    const rows = splitRows([{ kind: 'removed', text: 'a' }, { kind: 'added', text: 'b' }, { kind: 'added', text: 'c' }]);
    expect(rows.map(r => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([['a', 'b'], [null, 'c']]);
  });
});

describe('parseGitDiff', () => {
  it('parses files, hunks, line numbers and binaries', () => {
    const text = [
      'diff --git a/x.go b/x.go', 'index 1..2 100644', '--- a/x.go', '+++ b/x.go', '@@ -3,2 +3,2 @@ func x()', ' keep', '-old', '+new',
      'diff --git a/img.png b/img.png', 'Binary files a/img.png and b/img.png differ',
    ].join('\n');
    const files = parseGitDiff(text);
    expect(files.map(f => [f.path, f.binary])).toEqual([['x.go', false], ['img.png', true]]);
    expect(files[0].lines).toEqual([
      { kind: 'hunk', text: '@@ -3,2 +3,2 @@ func x()' },
      { kind: 'context', old_line: 3, new_line: 3, text: 'keep' },
      { kind: 'removed', old_line: 4, text: 'old' },
      { kind: 'added', new_line: 4, text: 'new' },
    ]);
    expect(parseGitDiff(undefined)).toEqual([]);
  });
});
