import { describe, expect, it } from 'vitest';
import { NODE_HEIGHT, NODE_WIDTH, layoutGraph } from './graphLayout';

describe('graph layout', () => {
  const rowOf = (layout: ReturnType<typeof layoutGraph>) => Object.fromEntries(layout.nodes.map(n => [n.id, n.rank]));

  it('puts importers above what they import and loose nodes at the bottom', () => {
    const layout = layoutGraph(['cmd', 'api', 'app', 'store', 'docs'], [
      { from: 'cmd', to: 'api', weight: 1 }, { from: 'api', to: 'app', weight: 1 }, { from: 'cmd', to: 'app', weight: 1 }, { from: 'app', to: 'store', weight: 3 },
    ]);
    expect(rowOf(layout)).toEqual({ cmd: 0, api: 1, app: 2, store: 3, docs: 4 });
  });

  it('breaks cycles and never overlaps nodes', () => {
    const layout = layoutGraph(['a', 'b', 'c', 'd'], [
      { from: 'a', to: 'b', weight: 1 }, { from: 'b', to: 'c', weight: 1 }, { from: 'c', to: 'a', weight: 1 }, { from: 'a', to: 'd', weight: 1 }, { from: 'b', to: 'b', weight: 1 }, { from: 'a', to: 'ghost', weight: 1 },
    ]);
    expect(layout.nodes).toHaveLength(4);
    for (const a of layout.nodes) {
      for (const b of layout.nodes) {
        if (a === b) continue;
        const apart = Math.abs(a.x - b.x) >= NODE_WIDTH || Math.abs(a.y - b.y) >= NODE_HEIGHT;
        expect(apart, `${a.id} overlaps ${b.id}`).toBe(true);
      }
    }
    expect(layout.width).toBeGreaterThan(NODE_WIDTH);
  });

  it('orders a row to follow the row above', () => {
    const layout = layoutGraph(['top1', 'top2', 'x', 'y'], [{ from: 'top1', to: 'y', weight: 1 }, { from: 'top2', to: 'x', weight: 1 }]);
    const x = (id: string) => layout.nodes.find(n => n.id === id)!.x;
    expect(x('top1') < x('top2')).toBe(true);
    expect(x('y') < x('x')).toBe(true);
  });
});
