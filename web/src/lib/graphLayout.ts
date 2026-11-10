// A layered layout for the components diagram: importers above what they
// import, nodes in a layer ordered to keep edges short, and each import
// routed as a path. A node is a card wide enough for a line on what the
// component does, so layers stack down the page. Small graphs only (the docs
// show at most 16 components), so the simple passes are enough.

export interface LayoutEdge {
  from: string;
  to: string;
  weight: number;
}

export interface LayoutNode {
  id: string;
  x: number;
  y: number;
  rank: number;
}

/** An import as drawn: its path, and whether it passes over layers between its ends. */
export interface RoutedEdge extends LayoutEdge {
  d: string;
  skips: boolean;
}

export interface Layout {
  nodes: LayoutNode[];
  edges: RoutedEdge[];
  width: number;
  height: number;
  nodeWidth: number;
}

export const NODE_HEIGHT = 86;
export const NODE_MIN_WIDTH = 164;
export const NODE_MAX_WIDTH = 440;
const GAP_X = 20;
const GAP_Y = 32;
const MARGIN = 4;
/** How far an edge that skips layers bows out beside them, for the first layer skipped and each further one. */
const BOW = 26;
const BOW_STEP = 14;

/**
 * Lays nodes out in rows: a node's row is one below the lowest row of the
 * nodes that import it. Cycles are broken where the search meets them;
 * nodes without edges get a row of their own at the bottom. Nodes are as
 * wide as `fit` allows for the widest row, within the card's limits; a row
 * too wide for `fit` makes the drawing wider than it.
 */
export function layoutGraph(ids: string[], edges: LayoutEdge[], fit = 720): Layout {
  const known = new Set(ids);
  const links = edges.filter(edge => known.has(edge.from) && known.has(edge.to) && edge.from !== edge.to);
  const acyclic = breakCycles(ids, links);
  const connected = new Set(links.flatMap(edge => [edge.from, edge.to]));
  const rank = longestPathRanks(ids.filter(id => connected.has(id)), acyclic);
  const bottom = Math.max(-1, ...rank.values()) + 1;
  for (const id of ids) if (!connected.has(id)) rank.set(id, bottom);
  const rows = orderRows(ids, rank, acyclic);
  const widest = Math.max(1, ...rows.map(row => row.length));
  const longest = Math.max(0, ...links.map(edge => Math.abs((rank.get(edge.to) ?? 0) - (rank.get(edge.from) ?? 0))));
  // Room beside the rows for edges that bow around the layers they skip.
  const room = longest > 1 ? BOW + BOW_STEP * (longest - 2) + 10 : 0;
  const sides = widest > 1 ? 2 : 1;
  const free = fit - MARGIN * 2 - room * sides - (widest - 1) * GAP_X;
  const nodeWidth = Math.round(Math.min(NODE_MAX_WIDTH, Math.max(NODE_MIN_WIDTH, free / widest)));
  const inner = widest * nodeWidth + (widest - 1) * GAP_X;
  const left = MARGIN + (sides === 2 ? room : 0);
  const nodes: LayoutNode[] = [];
  rows.forEach((row, r) => {
    const rowWidth = row.length * nodeWidth + (row.length - 1) * GAP_X;
    const start = left + (inner - rowWidth) / 2;
    row.forEach((id, i) => nodes.push({ id, rank: r, x: Math.round(start + i * (nodeWidth + GAP_X)), y: MARGIN + r * (NODE_HEIGHT + GAP_Y) }));
  });
  const position = new Map(nodes.map(node => [node.id, node]));
  const centre = left + inner / 2;
  return {
    nodes,
    edges: links.map(edge => routeEdge(edge, position.get(edge.from)!, position.get(edge.to)!, nodeWidth, centre)),
    width: left + inner + room + MARGIN,
    height: MARGIN * 2 + rows.length * NODE_HEIGHT + Math.max(0, rows.length - 1) * GAP_Y,
    nodeWidth,
  };
}

/**
 * An import's path. Between neighbouring layers it runs from the bottom of
 * one card to the top of the other. Over more layers it leaves and enters by
 * the cards' sides and bows out beside the rows, on the side its start is
 * nearer, so it never runs under the cards between.
 */
function routeEdge(edge: LayoutEdge, from: LayoutNode, to: LayoutNode, nodeWidth: number, centre: number): RoutedEdge {
  const span = Math.abs(to.rank - from.rank);
  if (span === 1) {
    const down = to.rank > from.rank;
    const x1 = from.x + nodeWidth / 2;
    const x2 = to.x + nodeWidth / 2;
    const y1 = down ? from.y + NODE_HEIGHT : from.y;
    const y2 = down ? to.y : to.y + NODE_HEIGHT;
    const bend = (y2 - y1) / 2;
    return { ...edge, skips: false, d: `M ${x1} ${y1} C ${x1} ${y1 + bend}, ${x2} ${y2 - bend}, ${x2} ${y2}` };
  }
  const right = from.x + nodeWidth / 2 >= centre;
  const x1 = right ? from.x + nodeWidth : from.x;
  const x2 = right ? to.x + nodeWidth : to.x;
  // Edges that leave one card for different layers leave it apart: the farther the layer, the lower the start.
  const way = to.rank > from.rank ? 1 : -1;
  const y1 = from.y + NODE_HEIGHT / 2 + way * Math.min(NODE_HEIGHT / 2 - 10, 14 * (span - 2));
  const y2 = to.y + NODE_HEIGHT / 2;
  // A cubic whose two control points sit `reach` out bows three quarters of it.
  const reach = ((BOW + BOW_STEP * Math.max(0, span - 2)) / 0.75) * (right ? 1 : -1);
  const out = right ? Math.max(x1, x2) : Math.min(x1, x2);
  return { ...edge, skips: true, d: `M ${x1} ${y1} C ${out + reach} ${y1}, ${out + reach} ${y2}, ${x2} ${y2}` };
}

/** The edges with each cycle broken: an edge back to a node on the search path is reversed. */
function breakCycles(ids: string[], edges: LayoutEdge[]): LayoutEdge[] {
  const out = new Map<string, LayoutEdge[]>();
  for (const edge of edges) out.set(edge.from, [...(out.get(edge.from) ?? []), edge]);
  const state = new Map<string, 'open' | 'done'>();
  const result: LayoutEdge[] = [];
  const visit = (id: string) => {
    state.set(id, 'open');
    for (const edge of out.get(id) ?? []) {
      const next = state.get(edge.to);
      if (next === 'open') result.push({ from: edge.to, to: edge.from, weight: edge.weight });
      else {
        result.push(edge);
        if (!next) visit(edge.to);
      }
    }
    state.set(id, 'done');
  };
  for (const id of ids) if (!state.has(id)) visit(id);
  return result;
}

function longestPathRanks(ids: string[], edges: LayoutEdge[]): Map<string, number> {
  const incoming = new Map(ids.map(id => [id, 0]));
  const out = new Map<string, string[]>();
  for (const edge of edges) {
    incoming.set(edge.to, (incoming.get(edge.to) ?? 0) + 1);
    out.set(edge.from, [...(out.get(edge.from) ?? []), edge.to]);
  }
  const rank = new Map(ids.map(id => [id, 0]));
  const queue = ids.filter(id => incoming.get(id) === 0);
  while (queue.length) {
    const id = queue.shift()!;
    for (const next of out.get(id) ?? []) {
      rank.set(next, Math.max(rank.get(next) ?? 0, (rank.get(id) ?? 0) + 1));
      incoming.set(next, (incoming.get(next) ?? 1) - 1);
      if (incoming.get(next) === 0) queue.push(next);
    }
  }
  return rank;
}

/** Rows of ids, each ordered by the mean position of its neighbours (a few sweeps down and up). */
function orderRows(ids: string[], rank: Map<string, number>, edges: LayoutEdge[]): string[][] {
  const rows: string[][] = [];
  for (const id of [...ids].sort()) {
    const r = rank.get(id) ?? 0;
    (rows[r] ??= []).push(id);
  }
  const compact = rows.filter(Boolean);
  const above = new Map<string, string[]>();
  const below = new Map<string, string[]>();
  for (const edge of edges) {
    below.set(edge.from, [...(below.get(edge.from) ?? []), edge.to]);
    above.set(edge.to, [...(above.get(edge.to) ?? []), edge.from]);
  }
  for (let sweep = 0; sweep < 4; sweep++) {
    const down = sweep % 2 === 0;
    const order = down ? compact.map((_, i) => i).slice(1) : compact.map((_, i) => i).reverse().slice(1);
    for (const r of order) {
      const reference = compact[down ? r - 1 : r + 1];
      const position = new Map(reference.map((id, i) => [id, i]));
      const neighbours = down ? above : below;
      const centre = (id: string) => {
        const known = (neighbours.get(id) ?? []).map(n => position.get(n)).filter((p): p is number => p !== undefined);
        return known.length ? known.reduce((a, b) => a + b, 0) / known.length : Number.POSITIVE_INFINITY;
      };
      compact[r] = [...compact[r]].sort((a, b) => centre(a) - centre(b) || a.localeCompare(b));
    }
  }
  return compact;
}
