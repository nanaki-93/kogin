// What a file does, for the code inspector: one sentence from its summary,
// the docs component it belongs to, and the rest of the summary on demand.

import { useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { useFileSummary, useOverview } from '@/api/hooks';
import { componentLabel, componentOf, firstSentence } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { timeAgo } from '@/lib/util';
import { Badge, cx } from '@/components/ui';

export function FileAbout({ path }: { path: string }) {
  const summary = useFileSummary(path).data;
  const components = useOverview().data?.docs?.components ?? [];
  const [open, setOpen] = useState(false);
  const component = componentOf(path, components);
  const usable = Boolean(summary?.purpose) && (summary?.status === 'fresh' || summary?.status === 'stale');
  if (!usable && !component) return null;
  const purpose = summary?.purpose ?? '';
  const lead = firstSentence(purpose, 240);
  const rest = purpose.replace(/\s+/g, ' ').trim().slice(lead.replace(/…$/, '').length).trim();
  const details = [...(summary?.responsibilities ?? []), ...(summary?.dependencies ?? []), ...(summary?.side_effects ?? [])].length + (rest ? 1 : 0);

  return (
    <section className="inspector-section stack" aria-label="About this file">
      <div className="row">
        <span className="strong small grow">About this file</span>
        {usable ? summary?.status === 'stale' ? <Badge tone="warning" title="Written for an older version of this file">Out of date</Badge> : <Badge tone="accent" title="From the analysis; written by a model">AI</Badge> : null}
      </div>
      {usable ? <p className="small">{lead}</p> : <p className="xs muted">No summary yet: an analysis writes one.</p>}
      {component ? (
        <div className="xs muted row-wrap">
          In
          <button type="button" className="chip mono inspector-chip" onClick={() => navigate('/docs/components', { path: component })} title="Open this component in Docs">{componentLabel({ path: component })}</button>
        </div>
      ) : null}
      {usable && details ? (
        <>
          <button type="button" className="link-btn xs" aria-expanded={open} onClick={() => setOpen(!open)}>
            <ChevronRight size={12} className={cx('chev', open && 'is-open')} aria-hidden /> {open ? 'Less' : 'More from the summary'}
          </button>
          {open ? (
            <div className="stack small file-about-more">
              {rest ? <p>{rest}</p> : null}
              <SummaryList title="Does" items={summary?.responsibilities} />
              <SummaryList title="Depends on" items={summary?.dependencies} />
              <SummaryList title="Side effects" items={summary?.side_effects} />
              {summary?.generated_at ? <div className="xs faint">From the analysis {timeAgo(summary.generated_at)}</div> : null}
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function SummaryList({ title, items }: { title: string; items?: string[] }) {
  if (!items?.length) return null;
  return (
    <div>
      <div className="xs muted">{title}</div>
      <ul>{items.map((item, i) => <li key={i}>{item}</li>)}</ul>
    </div>
  );
}
