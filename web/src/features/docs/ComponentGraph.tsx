// The architecture diagram: each component as a card that says what it does,
// in layers (importers above what they import), with the imports between
// them as arrows. The cards and arrows are read from the code; the line on
// what a component does is model-written.

import { useEffect, useMemo, useRef, useState } from 'react';
import { Play } from 'lucide-react';
import type { DocsComponent, DocsGraph } from '@/api/types';
import { NODE_HEIGHT, layoutGraph } from '@/lib/graphLayout';
import { cx } from '@/components/ui';
import { componentLabel, firstSentence } from '@/lib/docs';
import { plural } from '@/lib/util';

interface Props {
  components: DocsComponent[];
  graph: DocsGraph | undefined;
  /** What each component does, by path, where the architecture says; else its summary's first sentence. */
  roles?: ReadonlyMap<string, string>;
  /** The component whose page this is: its card and its imports stand out. */
  focus?: string;
  onOpen: (component: DocsComponent) => void;
}

export function ComponentGraph({ components, graph, roles, focus, onOpen }: Props) {
  const [hover, setHover] = useState<string | null>(null);
  const frame = useRef<HTMLDivElement>(null);
  const [fit, setFit] = useState(720);
  useEffect(() => {
    const element = frame.current;
    if (!element || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(([entry]) => { if (entry.contentRect.width > 0) setFit(Math.round(entry.contentRect.width)); });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const edges = useMemo(() => (graph?.edges ?? []).map(edge => ({ from: edge.from, to: edge.to, weight: edge.imports })), [graph]);
  const layout = useMemo(() => layoutGraph(components.map(c => c.path), edges, fit), [components, edges, fit]);
  if (!components.length) return <p className="muted">No components were found in the code yet.</p>;
  const byPath = new Map(components.map(c => [c.path, c]));
  const heaviest = Math.max(1, ...edges.map(edge => edge.weight));
  const lit = hover ?? focus ?? null;
  return (
    <figure className="component-graph" role="group" aria-label={`${plural(components.length, 'component')} and the imports between them`}>
      <div className="graph-frame" ref={frame}>
        <div className="graph-canvas" style={{ width: layout.width, height: layout.height }}>
          <svg viewBox={`0 0 ${layout.width} ${layout.height}`} width={layout.width} height={layout.height} aria-hidden>
            <defs>
              <marker id="graph-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="8" markerHeight="8" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" className="graph-arrow" />
              </marker>
              <marker id="graph-arrow-active" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="8" markerHeight="8" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" className="graph-arrow is-active" />
              </marker>
            </defs>
            {layout.edges.map(edge => {
              const active = lit === edge.from || lit === edge.to;
              return (
                <path key={`${edge.from}>${edge.to}`} d={edge.d} className={cx('graph-edge', active && 'is-active', lit && !active && 'is-dim')}
                  strokeWidth={1.25 + (1.75 * edge.weight) / heaviest} markerEnd={active ? 'url(#graph-arrow-active)' : 'url(#graph-arrow)'} />
              );
            })}
          </svg>
          {layout.nodes.map(node => {
            const component = byPath.get(node.id)!;
            const label = componentLabel(component);
            const role = firstSentence(roles?.get(component.path) ?? component.summary, 150);
            const uses = edges.filter(edge => edge.from === node.id).map(edge => `${edge.to} (${plural(edge.weight, 'import')})`);
            return (
              <button key={node.id} type="button" className={cx('graph-node', lit === node.id && 'is-active', focus === node.id && 'is-focus')}
                style={{ left: node.x, top: node.y, width: layout.nodeWidth, height: NODE_HEIGHT }}
                aria-label={`${label}: open its page`} aria-current={focus === node.id ? 'page' : undefined}
                title={[component.summary, uses.length ? `Imports ${uses.join(', ')}.` : ''].filter(Boolean).join('\n')}
                onClick={() => onOpen(component)} onMouseEnter={() => setHover(node.id)} onMouseLeave={() => setHover(null)} onFocus={() => setHover(node.id)} onBlur={() => setHover(null)}>
                <span className="graph-node-head">
                  <span className="graph-label mono truncate">{label}</span>
                  {component.entry_points?.length ? <span className="graph-start" title={`The program starts here: ${component.entry_points.map(entry => entry.path).join(', ')}`}><Play size={10} aria-hidden /> starts here</span> : null}
                  <span className="spacer" />
                  <span className="graph-meta">{plural(component.files ?? 0, 'file')}</span>
                </span>
                <span className="graph-role">{role || (component.languages ?? []).join(', ')}</span>
              </button>
            );
          })}
        </div>
      </div>
      <figcaption className="xs muted">
        Read from the code: an arrow goes from a component to one it imports, thicker for more imports.{graph?.truncated ? ' The project is large: some modules or imports are left out.' : ''}
      </figcaption>
    </figure>
  );
}
