import { describe, expect, it } from 'vitest';
import { buildMapTree, findNode, openIssues, squarify, trail, worstSeverity } from './treemap';

describe('project map', () => {
  const tree = buildMapTree(
    [
      { path: 'cmd/daemon/main.go', lines: 40, language: 'Go', analysis: 'fresh' },
      { path: 'internal/app/a.go', lines: 300, language: 'Go', issues: { high: 1 }, tasks: ['t1'], analysis: 'failed' },
      { path: 'internal/app/b.go', lines: 100, language: 'Go', issues: { low: 2 }, tasks: ['t1', 't2'] },
      { path: 'internal/api/h.go', lines: 60, language: 'Go' },
      { path: 'README.md', lines: 0, language: 'Markdown' },
    ],
    'kogin',
  );

  it('sums lines, issues, analysis and tasks up the tree, largest first', () => {
    expect(tree.lines).toBe(501);
    expect(tree.children.map(c => c.name)).toEqual(['internal', 'cmd/daemon', 'README.md']);
    const app = findNode(tree, 'internal/app');
    expect(app.path).toBe('internal/app');
    expect(openIssues(app)).toBe(3);
    expect(worstSeverity(app)).toBe('high');
    expect([...app.tasks].sort()).toEqual(['t1', 't2']);
    expect(tree.analysis).toEqual({ fresh: 1, failed: 1, missing: 3 });
  });

  it('merges a folder holding one folder and walks the trail to a folder', () => {
    expect(findNode(tree, 'cmd/daemon').name).toBe('cmd/daemon');
    expect(trail(tree, 'internal/app').map(n => n.name)).toEqual(['kogin', 'internal', 'app']);
  });

  it('fills the rectangle with cells close to squares', () => {
    const rects = squarify([6, 6, 4, 3, 2, 2, 1], { x: 0, y: 0, w: 6, h: 4 });
    const area = rects.reduce((sum, r) => sum + r.w * r.h, 0);
    expect(area).toBeCloseTo(24, 6);
    for (const r of rects) {
      expect(r.x).toBeGreaterThanOrEqual(-1e-9);
      expect(r.x + r.w).toBeLessThanOrEqual(6 + 1e-9);
      expect(r.y + r.h).toBeLessThanOrEqual(4 + 1e-9);
      expect(Math.max(r.w / r.h, r.h / r.w)).toBeLessThan(3);
    }
    expect(squarify([], { x: 0, y: 0, w: 10, h: 10 })).toEqual([]);
  });
});
