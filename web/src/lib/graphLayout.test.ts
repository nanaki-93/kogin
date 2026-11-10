import { describe, expect, it } from 'vitest';
import { NODE_HEIGHT, NODE_MAX_WIDTH, NODE_MIN_WIDTH, layoutGraph } from './graphLayout';

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
        const apart = Math.abs(a.x - b.x) >= layout.nodeWidth || Math.abs(a.y - b.y) >= NODE_HEIGHT;
        expect(apart, `${a.id} overlaps ${b.id}`).toBe(true);
      }
    }
    // An import of itself or of something outside the diagram is not drawn.
    expect(layout.edges.map(edge => `${edge.from}>${edge.to}`).sort()).toEqual(['a>b', 'a>d', 'b>c', 'c>a']);
  });

  it('orders a row to follow the row above', () => {
    const layout = layoutGraph(['top1', 'top2', 'x', 'y'], [{ from: 'top1', to: 'y', weight: 1 }, { from: 'top2', to: 'x', weight: 1 }]);
    const x = (id: string) => layout.nodes.find(n => n.id === id)!.x;
    expect(x('top1') < x('top2')).toBe(true);
    expect(x('y') < x('x')).toBe(true);
  });
});

describe('a chain of layers', () => {
  const chain = layoutGraph(['a', 'b', 'c', 'd'], [
    { from: 'a', to: 'b', weight: 1 }, { from: 'b', to: 'c', weight: 1 }, { from: 'c', to: 'd', weight: 1 }, { from: 'a', to: 'd', weight: 2 },
  ], 720);

  it('stacks down the page with cards as wide as a card may be', () => {
    const y = (id: string) => chain.nodes.find(n => n.id === id)!.y;
    expect(y('a') < y('b') && y('b') < y('c') && y('c') < y('d')).toBe(true);
    expect(chain.nodeWidth).toBe(NODE_MAX_WIDTH);
    expect(chain.width).toBeLessThanOrEqual(720);
  });

  it('routes an import over layers beside the cards, not under them', () => {
    const skip = chain.edges.find(edge => edge.from === 'a' && edge.to === 'd')!;
    const next = chain.edges.find(edge => edge.from === 'a' && edge.to === 'b')!;
    expect(skip.skips).toBe(true);
    expect(next.skips).toBe(false);
    const right = chain.nodes[0].x + chain.nodeWidth;
    // The path leaves by the card's right side and its control points lie outside every card.
    expect(skip.d.startsWith(`M ${right} `)).toBe(true);
    const controls = [...skip.d.matchAll(/(-?[\d.]+) (-?[\d.]+)/g)].slice(1, 3).map(match => Number(match[1]));
    for (const x of controls) expect(x).toBeGreaterThan(right);
    expect(chain.width).toBeGreaterThan(right + 20);
  });
});

describe('a wide layer', () => {
  it('narrows the cards to fit, down to the least a card can be', () => {
    const ids = ['top', 'a', 'b', 'c'];
    const three = layoutGraph(ids, ids.slice(1).map(to => ({ from: 'top', to, weight: 1 })), 720);
    expect(three.nodeWidth).toBeLessThan(NODE_MAX_WIDTH);
    expect(three.width).toBeLessThanOrEqual(720);
    const many = ['top', ...Array.from({ length: 8 }, (_, i) => `n${i}`)];
    const eight = layoutGraph(many, many.slice(1).map(to => ({ from: 'top', to, weight: 1 })), 720);
    expect(eight.nodeWidth).toBe(NODE_MIN_WIDTH);
    expect(eight.width).toBeGreaterThan(720);
  });
});
