// The architecture diagram: each component as a card that says what it does,
// in layers (importers above what they import), with the imports between
// them as arrows, optional Coupling (Martin Instability I) & AGENTS.md
// Boundary lenses, and live Execution Flow step highlighting.

import { useEffect, useMemo, useRef, useState } from 'react';
import { Play, ShieldAlert } from 'lucide-react';
import type { DocsComponent, DocsGraph } from '@/api/types';
import type { ComponentCoupling, StabilityInversion } from '@/lib/architectureLens';
import { NODE_HEIGHT, layoutGraph } from '@/lib/graphLayout';
import { cx } from '@/components/ui';
import { componentLabel, firstSentence } from '@/lib/docs';
import { plural } from '@/lib/util';

export type GraphLens = 'topology' | 'coupling' | 'boundaries';

interface Props {
  components: DocsComponent[];
  graph: DocsGraph | undefined;
  /** What each component does, by path, where the architecture says; else its summary's first sentence. */
  roles?: ReadonlyMap<string, string>;
  /** The component whose page or inspection lens is active: its card and ties stand out. */
  focus?: string;
  /** Active visual analysis lens on Architecture Studio. */
  lens?: GraphLens;
  /** Static coupling & boundary metrics keyed by component path. */
  metrics?: ReadonlyMap<string, ComponentCoupling>;
  /** Edges violating the Stable Dependencies Principle (stable importing volatile). */
  inversions?: StabilityInversion[];
  /** Ordered component paths traversed by an active Execution Flow walkthrough. */
  activeTrail?: string[];
  onOpen: (component: DocsComponent) => void;
}

export function ComponentGraph({
  components,
  graph,
  roles,
  focus,
  lens = 'topology',
  metrics,
  inversions = [],
  activeTrail = [],
  onOpen,
}: Props) {
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
  const litMetric = lit ? metrics?.get(lit) : undefined;
  const inversionSet = new Set(inversions.map(inv => `${inv.from}>${inv.to}`));
  const trailSet = new Set(activeTrail);

  const isForbiddenTarget = (candidatePath: string): boolean => {
    if (lens !== 'boundaries' || !litMetric) return false;
    return litMetric.forbiddenImports.some(rule => candidatePath === rule.never || candidatePath.startsWith(rule.never + '/') || rule.never.startsWith(candidatePath + '/'));
  };

  return (
    <figure className={cx('component-graph', `lens-${lens}`)} role="group" aria-label={`${plural(components.length, 'component')} and the imports between them`}>
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
              <marker id="graph-arrow-warn" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="8" markerHeight="8" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" className="graph-arrow is-warn" />
              </marker>
            </defs>
            {layout.edges.map(edge => {
              const onTrail = trailSet.has(edge.from) && trailSet.has(edge.to);
              const active = lit === edge.from || lit === edge.to || onTrail;
              const inverted = lens === 'coupling' && inversionSet.has(`${edge.from}>${edge.to}`);
              return (
                <path
                  key={`${edge.from}>${edge.to}`}
                  d={edge.d}
                  className={cx('graph-edge', active && 'is-active', inverted && 'is-warn', (lit || trailSet.size > 0) && !active && 'is-dim')}
                  strokeWidth={1.25 + (1.75 * edge.weight) / heaviest}
                  markerEnd={inverted ? 'url(#graph-arrow-warn)' : active ? 'url(#graph-arrow-active)' : 'url(#graph-arrow)'}
                />
              );
            })}
          </svg>
          {layout.nodes.map(node => {
            const component = byPath.get(node.id)!;
            const label = componentLabel(component);
            const role = firstSentence(roles?.get(component.path) ?? component.summary, 150);
            const uses = edges.filter(edge => edge.from === node.id).map(edge => `${edge.to} (${plural(edge.weight, 'import')})`);
            const metric = metrics?.get(component.path);
            const forbidden = isForbiddenTarget(component.path);
            const inTrail = trailSet.has(node.id);
            const stepIndex = activeTrail.indexOf(node.id);

            return (
              <button
                key={node.id}
                type="button"
                className={cx(
                  'graph-node',
                  metric && lens === 'coupling' && `role-${metric.roleKind}`,
                  (lit === node.id || inTrail) && 'is-active',
                  focus === node.id && 'is-focus',
                  forbidden && 'is-forbidden',
                )}
                style={{ left: node.x, top: node.y, width: layout.nodeWidth, height: NODE_HEIGHT }}
                aria-label={`${label}: open its page`}
                aria-current={focus === node.id ? 'page' : undefined}
                title={[component.summary, uses.length ? `Imports ${uses.join(', ')}.` : ''].filter(Boolean).join('\n')}
                onClick={() => onOpen(component)}
                onMouseEnter={() => setHover(node.id)}
                onMouseLeave={() => setHover(null)}
                onFocus={() => setHover(node.id)}
                onBlur={() => setHover(null)}
              >
                <span className="graph-node-head">
                  {stepIndex >= 0 ? <span className="graph-step-num mono">{stepIndex + 1}</span> : null}
                  <span className="graph-label mono truncate">{label}</span>
                  {lens === 'topology' && component.entry_points?.length ? (
                    <span className="graph-start" title={`The program starts here: ${component.entry_points.map(entry => entry.path).join(', ')}`}>
                      <Play size={10} aria-hidden /> starts here
                    </span>
                  ) : null}
                  {lens === 'coupling' && metric ? (
                    <span className={cx('graph-pill', 'mono', `tone-role-${metric.roleKind}`)} title={`Ca=${metric.ca} callers, Ce=${metric.ce} imports · ${metric.roleLabel}`}>
                      {metric.instability !== null ? `I=${metric.instability.toFixed(2)}` : 'isolated'}
                    </span>
                  ) : null}
                  {lens === 'boundaries' && metric ? (
                    forbidden ? (
                      <span className="graph-pill is-forbidden" title="Forbidden import target for the inspected component">
                        <ShieldAlert size={10} aria-hidden /> forbidden
                      </span>
                    ) : metric.forbiddenImports.length ? (
                      <span className="graph-pill" title={`Guarded by ${plural(metric.forbiddenImports.length, 'AGENTS.md rule')}`}>
                        {plural(metric.forbiddenImports.length, 'rule')}
                      </span>
                    ) : null
                  ) : null}
                  <span className="spacer" />
                  <span className="graph-meta">{plural(component.files ?? 0, 'file')}</span>
                </span>
                <span className="graph-role">
                  {lens === 'coupling' && metric
                    ? `${metric.roleLabel} · Ca ${metric.ca} / Ce ${metric.ce}`
                    : role || (component.languages ?? []).join(', ')}
                </span>
              </button>
            );
          })}
        </div>
      </div>
      <figcaption className="xs muted">
        Read from the code: an arrow goes from a component to one it imports, thicker for more imports.{graph?.truncated ? ' The project is large: some modules or imports are left out.' : ''}
        {lens === 'coupling' ? ' Instability I = Ce / (Ca + Ce): 0.00 is a stable foundational core; 1.00 is a volatile entry or orchestrator.' : ''}
        {lens === 'boundaries' ? ' Inspect or hover a component to highlight packages its AGENTS.md rules forbid importing.' : ''}
      </figcaption>
    </figure>
  );
}
