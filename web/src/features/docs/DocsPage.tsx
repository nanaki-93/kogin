// The generated handbook: purpose and topology, components by layer,
// dependencies, runbook, scenarios, decisions and glossary.

import { BookOpen, Boxes, Compass, GitFork, ListOrdered, MessageCircleQuestion, Network, RefreshCw, Terminal, TextQuote } from 'lucide-react';
import { useOverview, useProject } from '@/api/hooks';
import type { DocsComponent, DocsRefresh, ProjectDocs } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { navigate, useRoute } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { AskPanel } from './AskPanel';
import { ComponentGraph } from './ComponentGraph';
import { Badge, Button, Callout, EmptyState, Skeleton, Spinner, cx } from '@/components/ui';
import { useFeedback } from '@/components/overlays';

const SECTIONS = [
  { id: 'overview', label: 'Overview', icon: Compass },
  { id: 'components', label: 'Components', icon: Boxes },
  { id: 'dependencies', label: 'Dependencies', icon: GitFork },
  { id: 'scenarios', label: 'Scenarios', icon: Network },
  { id: 'decisions', label: 'Decisions', icon: ListOrdered },
  { id: 'runbook', label: 'Runbook', icon: Terminal },
  { id: 'glossary', label: 'Glossary', icon: TextQuote },
  { id: 'ask', label: 'Ask the code', icon: MessageCircleQuestion },
] as const;

export function DocsPage({ section }: { section: string }) {
  const { data, isLoading } = useOverview();
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = useBusy();
  const docs = data?.docs;
  const current = SECTIONS.find(s => s.id === section)?.id ?? 'overview';

  return (
    <div className="docs">
      <nav className="docs-nav" aria-label="Docs sections">
        <div className="panel-title">Handbook</div>
        {SECTIONS.map(s => (
          <a key={s.id} href={`#/docs/${s.id}`} className={cx('docs-link', s.id === current && 'is-active')} onClick={e => { e.preventDefault(); navigate(`/docs/${s.id}`); }}>
            <s.icon size={14} aria-hidden />
            {s.label}
            <span className="spacer" />
            <span className="xs faint">{countOf(docs, s.id)}</span>
          </a>
        ))}
        <div className="docs-refresh">
          {docs?.updated_at ? <div className="xs faint">Updated {timeAgo(docs.updated_at)}</div> : null}
          <RefreshStatus {...latestRefresh(data?.docs_refresh, data?.analysis_run?.docs)} />
          {project && docs ? (
            <Button size="sm" variant="ghost" icon={RefreshCw} disabled={Boolean(busy) || data?.docs_refresh?.status === 'running'} onClick={() => void actions.refreshDocs(project, worktreePath)}
              title="Summarize again the components whose files changed, then write the overview again">
              Refresh docs
            </Button>
          ) : null}
        </div>
      </nav>
      <div className="docs-body page-scroll">
        <div className="page-inner page-narrow">
          {current === 'ask' ? (
            <AskPanel project={project} worktreePath={worktreePath} />
          ) : isLoading ? (
            <Skeleton lines={10} />
          ) : !docs ? (
            <EmptyState icon={BookOpen} title="No handbook yet" action={project ? <Button variant="primary" onClick={() => void actions.runAnalysis(project, worktreePath)} disabled={Boolean(busy)}>Analyze project</Button> : undefined}>
              Project analysis writes this handbook from the source: purpose, components, dependencies and how to run things.
            </EmptyState>
          ) : (
            <>
              {docs.status && docs.status !== 'fresh' && docs.status !== 'completed' ? (
                <div className="docs-notice">
                  <Callout tone="warning" title={DOCS_STATUS_TITLES[docs.status] ?? 'The project overview is incomplete'}>
                    {DOCS_STATUS_DETAILS[docs.status] ?? 'Sections below come from indexed facts only.'}
                  </Callout>
                </div>
              ) : null}
              <DocsSection docs={docs} section={current} />
            </>
          )}
        </div>
      </div>
    </div>
  );
}

/** The most recent refresh: the Docs page's own or the one that ended the last run. */
function latestRefresh(own?: DocsRefresh, run?: DocsRefresh | null): { refresh?: DocsRefresh; fromRun: boolean } {
  if (own?.status === 'running' || !run) return { refresh: own, fromRun: false };
  return !own || Date.parse(run.at) > Date.parse(own.at) ? { refresh: run, fromRun: true } : { refresh: own, fromRun: false };
}

const OVERVIEW_OUTCOMES: Record<string, string> = { written: 'overview written', kept: 'overview kept', failed: 'overview failed' };

function RefreshStatus({ refresh, fromRun }: { refresh?: DocsRefresh; fromRun: boolean }) {
  if (!refresh) return null;
  if (refresh.status === 'running') {
    return <div className="xs muted row" role="status"><Spinner size={12} label="Refreshing" /> Refreshing the docs…</div>;
  }
  const by = fromRun ? 'Last run' : 'Refresh';
  if (refresh.status === 'failed' || refresh.status === 'skipped') {
    return <div className={cx('xs', refresh.status === 'failed' ? 'tone-danger' : 'muted')} role="status">{by} {refresh.status === 'failed' ? 'failed' : 'skipped'} {timeAgo(refresh.at)}{refresh.reason ? `: ${refresh.reason}` : ''}</div>;
  }
  const parts = [`${plural(refresh.summarized, 'summary', 'summaries')} written`, refresh.kept ? `${refresh.kept} kept` : '', OVERVIEW_OUTCOMES[refresh.overview] ?? ''].filter(Boolean);
  return <div className="xs faint" role="status">{by} {timeAgo(refresh.at)}: {parts.join(', ')}</div>;
}

function countOf(docs: ProjectDocs | undefined, id: string): string {
  if (!docs || id === 'overview') return '';
  const list = (docs as unknown as Record<string, unknown[]>)[id];
  return Array.isArray(list) && list.length ? String(list.length) : '';
}

function DocsSection({ docs, section }: { docs: ProjectDocs; section: string }) {
  const { toast } = useFeedback();
  switch (section) {
    case 'components':
      return <Components components={docs.components ?? []} />;
    case 'dependencies':
      return (
        <article className="doc">
          <h1>Dependencies</h1>
          <table className="table">
            <thead><tr><th>Name</th><th>Kind</th><th>Version</th><th>Source</th></tr></thead>
            <tbody>{(docs.dependencies ?? []).map(d => (<tr key={d.name}><td><div className="strong-row">{d.name}</div>{d.summary ? <div className="xs muted">{d.summary}</div> : null}</td><td className="small">{d.kind}</td><td className="mono xs">{d.version}</td><td className="mono xs muted">{d.source}</td></tr>))}</tbody>
          </table>
        </article>
      );
    case 'scenarios':
      return (
        <article className="doc">
          <h1>Scenarios</h1>
          {(docs.scenarios ?? []).length ? (docs.scenarios ?? []).map(s => (
            <section key={s.title} className="doc-card">
              <h2>{s.title}</h2>
              {s.summary ? <p>{s.summary}</p> : null}
              {s.invariant ? <p className="small"><span className="muted">Invariant: </span>{s.invariant}</p> : null}
              {s.steps?.length ? <ol className="steps">{s.steps.map((step, i) => (<li key={i}><span className="strong">{step.title}</span>{step.detail ? <span className="muted"> — {step.detail}</span> : null}{step.path ? <button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: step.path })}>{step.path}</button> : null}</li>))}</ol> : null}
            </section>
          )) : <p className="muted">No scenarios were documented yet.</p>}
        </article>
      );
    case 'decisions':
      return (
        <article className="doc">
          <h1>Decisions</h1>
          {(docs.decisions ?? []).length ? (docs.decisions ?? []).map(d => (
            <section key={d.title} className="doc-card">
              <div className="row"><h2 className="grow">{d.title}</h2>{d.category ? <Badge>{d.category}</Badge> : null}</div>
              {d.decision ? <p><span className="muted">Decision: </span>{d.decision}</p> : null}
              {d.tradeoff ? <p><span className="muted">Tradeoff: </span>{d.tradeoff}</p> : null}
            </section>
          )) : <p className="muted">No decisions were recorded yet.</p>}
        </article>
      );
    case 'runbook':
      return (
        <article className="doc">
          <h1>Runbook</h1>
          <div className="stack">
            {(docs.runbook ?? []).map(r => (
              <div key={r.label} className="runbook-item">
                <div className="grow">
                  <div className="strong">{r.label}</div>
                  {r.description ? <div className="small muted">{r.description}</div> : null}
                  <code className="command">{r.command}</code>
                </div>
                <Button size="sm" variant="ghost" onClick={() => { void navigator.clipboard?.writeText(r.command); toast('Command copied.', 'success'); }}>Copy</Button>
              </div>
            ))}
          </div>
        </article>
      );
    case 'glossary':
      return (
        <article className="doc">
          <h1>Glossary</h1>
          <dl className="glossary">{(docs.glossary ?? []).map(g => (<div key={g.term}><dt>{g.term}</dt><dd>{g.definition}</dd></div>))}</dl>
        </article>
      );
    default:
      return (
        <article className="doc">
          <h1>Overview</h1>
          {docs.context?.purpose ? <p className="lead">{docs.context.purpose}</p> : null}
          <div className="facts">
            {docs.context?.topology ? <div><div className="xs muted">Topology</div><div>{docs.context.topology}</div></div> : null}
            {docs.context?.stack?.length ? <div><div className="xs muted">Stack</div><div>{docs.context.stack.join(', ')}</div></div> : null}
          </div>
          {docs.context?.goal_pursuit?.length ? (<><h2>Goals</h2><ul>{docs.context.goal_pursuit.map((g, i) => <li key={i}>{g}</li>)}</ul></>) : null}
          <h2>Components</h2>
          <ComponentGraph components={docs.components ?? []} graph={docs.graph} onOpen={c => navigate('/docs/components', { path: c.path })} />
          {docs.graph?.entry_points?.length ? (
            <>
              <h2>Where it starts</h2>
              <ul className="entry-points">
                {docs.graph.entry_points.map(e => (
                  <li key={`${e.path}:${e.detail ?? ''}`}>
                    <button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: e.path })}>{e.path}</button>
                    <span className="xs muted"> {entryLabel(e.kind, e.detail)}</span>
                  </li>
                ))}
              </ul>
            </>
          ) : null}
        </article>
      );
  }
}

function groupByLayer(components: DocsComponent[]): [string, DocsComponent[]][] {
  const map = new Map<string, DocsComponent[]>();
  for (const c of components) map.set(c.layer || 'Other', [...(map.get(c.layer || 'Other') ?? []), c]);
  return [...map.entries()];
}

function entryLabel(kind: string, detail?: string): string {
  if (kind === 'main') return detail ? `${detail} main` : 'main';
  if (kind === 'script') return `script ${detail ?? ''}`.trim();
  return detail ? `${kind} ${detail}` : kind;
}

const SUMMARY_SOURCES: Record<string, string> = {
  module: 'Model summary of its files',
  overview: 'From the project overview (model)',
};

function Components({ components }: { components: DocsComponent[] }) {
  const route = useRoute();
  const focus = route.params.get('path');
  const byPath = new Map(components.map(c => [c.path, c]));
  const chip = (path: string) => (
    <button key={path} type="button" className="chip" onClick={() => navigate('/docs/components', { path })} title={path}>{byPath.get(path)?.name ?? path}</button>
  );
  return (
    <article className="doc">
      <h1>Components</h1>
      <p className="small muted">The directories of code, grouped so at most sixteen show; what each imports is read from the code.</p>
      {groupByLayer(components).map(([layer, list]) => (
        <section key={layer}>
          <h2>{layer}</h2>
          <div className="component-grid">
            {list.map(c => (
              <div key={c.path} id={`component-${c.path}`} className={cx('doc-card', c.path === focus && 'is-focus')}>
                <div className="row"><span className="strong grow">{c.name}</span><span className="mono xs faint">{c.path}</span></div>
                <div className="xs muted">{[`${c.files ?? 0} files`, c.lines ? `${c.lines} lines` : '', (c.languages ?? []).join(', ')].filter(Boolean).join(' · ')}</div>
                {c.summary ? (
                  <>
                    <p className="small">{c.summary}</p>
                    {c.summary_source ? <div className="xs faint">{SUMMARY_SOURCES[c.summary_source] ?? 'Model-written'}</div> : null}
                  </>
                ) : <p className="small muted">No summary yet: an analysis run writes one from its files.</p>}
                {c.responsibilities?.length ? <ul className="small">{c.responsibilities.slice(0, 4).map((r, i) => <li key={i}>{r}</li>)}</ul> : null}
                {c.dependencies?.length ? <div className="chips"><span className="xs muted">Imports</span>{c.dependencies.map(chip)}</div> : null}
                {c.used_by?.length ? <div className="chips"><span className="xs muted">Used by</span>{c.used_by.map(chip)}</div> : null}
                {c.contracts?.length ? (
                  <div className="chips">
                    <span className="xs muted">Starts at</span>
                    {c.contracts.map(e => <button key={e.name + e.summary} type="button" className="chip chip-file" onClick={() => navigate('/code', { file: e.name })} title={e.summary}>{e.name}</button>)}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        </section>
      ))}
    </article>
  );
}

// The docs' status is the project overview's: the model-written purpose,
// architecture and flows. The components, their imports, entry points,
// dependencies and runbook are read from the code on every visit.
const DOCS_STATUS_TITLES: Record<string, string> = {
  unavailable: 'No project overview yet',
  failed: 'The project overview could not be written',
  stale: 'The project overview is out of date',
};

const DOCS_STATUS_DETAILS: Record<string, string> = {
  unavailable: 'The components, imports, entry points, dependencies and runbook below are read from the code. The model-written overview of purpose and flows has not been written for this project.',
  failed: 'Sections below come from indexed facts only. The analyze model did not return a usable overview.',
  stale: 'The model-written overview was written for an older version of the code; the components, imports and runbook are current.',
};
