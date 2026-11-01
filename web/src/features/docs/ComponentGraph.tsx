// The components diagram: each component of the project and the imports
// between them, laid out from the code (importers above what they import).

import { useMemo, useState } from 'react';
import type { DocsComponent, DocsGraph } from '@/api/types';
import { NODE_HEIGHT, NODE_WIDTH, layoutGraph } from '@/lib/graphLayout';
import { cx } from '@/components/ui';
import { componentLabel } from '@/lib/docs';

export function ComponentGraph({ components, graph, onOpen }: { components: DocsComponent[]; graph: DocsGraph | undefined; onOpen: (component: DocsComponent) => void }) {
  const [hover, setHover] = useState<string | null>(null);
  const edges = useMemo(() => (graph?.edges ?? []).map(edge => ({ from: edge.from, to: edge.to, weight: edge.imports })), [graph]);
  const layout = useMemo(() => layoutGraph(components.map(c => c.path), edges), [components, edges]);
  if (!components.length) return <p className="muted">No components were found in the code yet.</p>;
  const position = new Map(layout.nodes.map(node => [node.id, node]));
  const byPath = new Map(components.map(c => [c.path, c]));
  const heaviest = Math.max(1, ...edges.map(edge => edge.weight));
  return (
    <figure className="component-graph">
      <svg viewBox={`0 0 ${layout.width} ${layout.height}`} role="img" aria-label={`${components.length} components and the imports between them`} style={{ maxWidth: layout.width }}>
        <defs>
          <marker id="graph-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="9" markerHeight="9" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" className="graph-arrow" />
          </marker>
        </defs>
        {edges.map(edge => {
          const from = position.get(edge.from);
          const to = position.get(edge.to);
          if (!from || !to) return null;
          // An edge that skips rows leaves and enters on the right and bows
          // out, so it does not run through the nodes between.
          const span = Math.abs(to.rank - from.rank);
          const side = span > 1 ? NODE_WIDTH * 0.8 : NODE_WIDTH / 2;
          const bow = span > 1 ? 36 + 24 * (span - 1) : 0;
          const x1 = from.x + side;
          const x2 = to.x + side;
          const downward = to.y > from.y;
          const y1 = downward ? from.y + NODE_HEIGHT : from.y;
          const y2 = downward ? to.y : to.y + NODE_HEIGHT;
          const bend = (y2 - y1) / 2 || 40;
          const active = hover === edge.from || hover === edge.to;
          return (
            <path key={`${edge.from}>${edge.to}`} d={`M ${x1} ${y1} C ${x1 + bow} ${y1 + bend}, ${x2 + bow} ${y2 - bend}, ${x2} ${y2}`} className={cx('graph-edge', active && 'is-active', hover && !active && 'is-dim')}
              strokeWidth={1 + (2 * edge.weight) / heaviest} markerEnd="url(#graph-arrow)">
              <title>{`${edge.from} imports ${edge.to} (${edge.weight} ${edge.weight === 1 ? 'import' : 'imports'})`}</title>
            </path>
          );
        })}
        {layout.nodes.map(node => {
          const component = byPath.get(node.id)!;
          const path = componentLabel(component);
          const label = path.length > 20 ? '…' + path.slice(-19) : path;
          return (
            <g key={node.id} className={cx('graph-node', hover === node.id && 'is-active')} transform={`translate(${node.x} ${node.y})`} tabIndex={0} role="button"
              aria-label={`${path}: open its page`} onClick={() => onOpen(component)} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(component); } }}
              onMouseEnter={() => setHover(node.id)} onMouseLeave={() => setHover(null)} onFocus={() => setHover(node.id)} onBlur={() => setHover(null)}>
              <title>{`${component.path}${component.summary ? `: ${component.summary}` : ''}`}</title>
              <rect width={NODE_WIDTH} height={NODE_HEIGHT} rx={8} />
              <text x={12} y={19} className="graph-label">{label}</text>
              <text x={12} y={35} className="graph-meta">{`${component.files ?? 0} files${component.languages?.length ? ` · ${component.languages[0]}` : ''}`}</text>
            </g>
          );
        })}
      </svg>
      <figcaption className="xs muted">
        Drawn from the code: an arrow goes from a component to one it imports; thicker arrows carry more imports.{graph?.truncated ? ' The project is large: some modules or imports are left out.' : ''}
      </figcaption>
    </figure>
  );
}
