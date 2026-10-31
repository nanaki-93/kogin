// A layered layout for the components diagram: importers above what they
// import, nodes in a row ordered to keep edges short. Small graphs only (the
// docs show at most 16 components), so the simple passes are enough.

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

export interface Layout {
  nodes: LayoutNode[];
  width: number;
  height: number;
}

export const NODE_WIDTH = 156;
export const NODE_HEIGHT = 46;
const GAP_X = 28;
const GAP_Y = 64;
const MARGIN = 12;

/**
 * Lays nodes out in rows: a node's row is one below the lowest row of the
 * nodes that import it. Cycles are broken where the search meets them;
 * nodes without edges get a row of their own at the bottom.
 */
export function layoutGraph(ids: string[], edges: LayoutEdge[]): Layout {
  const known = new Set(ids);
  const links = edges.filter(edge => known.has(edge.from) && known.has(edge.to) && edge.from !== edge.to);
  const acyclic = breakCycles(ids, links);
  const connected = new Set(links.flatMap(edge => [edge.from, edge.to]));
  const rank = longestPathRanks(ids.filter(id => connected.has(id)), acyclic);
  const bottom = Math.max(-1, ...rank.values()) + 1;
  for (const id of ids) if (!connected.has(id)) rank.set(id, bottom);
  const rows = orderRows(ids, rank, acyclic);
  const widest = Math.max(1, ...rows.map(row => row.length));
  // Room on the right for edges that bow around the rows they skip.
  const width = MARGIN * 2 + widest * NODE_WIDTH + (widest - 1) * GAP_X + 48;
  const nodes: LayoutNode[] = [];
  rows.forEach((row, r) => {
    const rowWidth = row.length * NODE_WIDTH + (row.length - 1) * GAP_X;
    const left = (width - rowWidth) / 2;
    row.forEach((id, i) => nodes.push({ id, rank: r, x: left + i * (NODE_WIDTH + GAP_X), y: MARGIN + r * (NODE_HEIGHT + GAP_Y) }));
  });
  return { nodes, width, height: MARGIN * 2 + rows.length * NODE_HEIGHT + Math.max(0, rows.length - 1) * GAP_Y };
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
