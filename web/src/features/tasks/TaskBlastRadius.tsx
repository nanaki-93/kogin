// Compact architectural blast-radius strip for a task or proposed plan:
// [Callers (used by)] -> [Touched component(s)] -> [Dependencies (imports)].
// Purely read-only over cached overview docs; clicking a module opens its page.

import { useMemo } from 'react';
import { ArrowRight, Layers } from 'lucide-react';
import type { DocsComponent } from '@/api/types';
import { useOverview } from '@/api/hooks';
import { computeBlastRadius } from '@/lib/blastRadius';
import { componentLabel, firstSentence } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';

function ModuleNode({ component, active }: { component: DocsComponent; active?: boolean }) {
  const label = componentLabel(component);
  const role = firstSentence(component.summary, 84);
  return (
    <button
      type="button"
      className={active ? 'blast-node is-touched' : 'blast-node'}
      onClick={() => navigate('/docs/components', { path: component.path })}
      title={role ? `${label} — ${role}` : `Open ${label} in Docs`}
    >
      <span className="blast-node-name mono truncate">{label}</span>
      {active && role ? <span className="blast-node-role xs muted truncate">{role}</span> : null}
    </button>
  );
}

function ModuleColumn({ heading, items, emptyLabel, active }: { heading: string; items: DocsComponent[]; emptyLabel: string; active?: boolean }) {
  const visible = items.slice(0, 3);
  const hidden = items.length - visible.length;
  return (
    <div className="blast-col">
      <span className="blast-col-label xs faint">{heading}</span>
      {visible.length ? (
        <div className="blast-nodes">
          {visible.map(item => (
            <ModuleNode key={item.path} component={item} active={active} />
          ))}
          {hidden > 0 ? <span className="xs faint">+{hidden} more</span> : null}
        </div>
      ) : (
        <span className="blast-empty xs faint">{emptyLabel}</span>
      )}
    </div>
  );
}

export function TaskBlastRadius({ paths }: { paths: string[] }) {
  const components = useOverview().data?.docs?.components;
  const radius = useMemo(() => computeBlastRadius(paths, components), [paths, components]);
  if (!radius) return null;

  const summary = [
    radius.callers.length ? plural(radius.callers.length, 'caller') : 'top-level',
    plural(radius.touched.length, 'touched module'),
    radius.dependencies.length ? plural(radius.dependencies.length, 'dependency', 'dependencies') : 'leaf',
  ].join(' · ');

  return (
    <section className="blast-radius" aria-label="Blast radius">
      <div className="blast-head">
        <Layers size={13} aria-hidden />
        <span className="xs strong">Blast radius</span>
        <span className="xs muted">{summary}</span>
      </div>
      <div className="blast-flow">
        <ModuleColumn heading="Used by" items={radius.callers} emptyLabel="Entry / none" />
        <ArrowRight size={13} className="blast-arrow faint" aria-hidden />
        <ModuleColumn heading="Touches" items={radius.touched} emptyLabel="None" active />
        <ArrowRight size={13} className="blast-arrow faint" aria-hidden />
        <ModuleColumn heading="Imports" items={radius.dependencies} emptyLabel="Leaf module" />
      </div>
    </section>
  );
}
