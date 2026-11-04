// The docs: a site-like guide to the project. The nav lists the guide pages,
// every walkthrough and every component; each opens its own page. What comes
// from the code and what a model wrote stay marked apart.

import { useState, type ReactNode } from 'react';
import { BookOpen, Boxes, ChevronRight, Compass, Layers, Library, MessageCircleQuestion, RefreshCw, Route as RouteIcon, Terminal, Wrench } from 'lucide-react';
import { useOverview, useProject } from '@/api/hooks';
import type { DocsRefresh, ProjectDocs } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { componentLabel, docsSection, type DocsSection } from '@/lib/docs';
import { navigate, useRoute } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { Button, EmptyState, Input, Skeleton, Spinner, cx } from '@/components/ui';
import { AskPanel, handQuestion } from './AskPanel';
import { ArchitecturePage, DependenciesPage, OverviewPage, StartPage } from './GuidePages';
import { ComponentPage, ComponentsIndex } from './ComponentPage';
import { DocRefsProvider } from './DocText';
import { ChangeGuideIndex, ChangeGuidePage, WalkthroughPage, WalkthroughsIndex } from './WalkthroughPage';

export function DocsPage({ section }: { section: string }) {
  const { data, isLoading } = useOverview();
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const route = useRoute();
  const docs = data?.docs;
  const current = docsSection(section);

  return (
    <div className="docs">
      <DocsNav docs={docs} current={current} />
      <div className="docs-body page-scroll">
        <div className="page-inner page-narrow">
          {current === 'ask' ? (
            <AskPanel project={project} worktreePath={worktreePath} />
          ) : isLoading ? (
            <Skeleton lines={10} />
          ) : !docs ? (
            <EmptyState icon={BookOpen} title="No docs yet" action={<Button variant="primary" onClick={() => navigate('/analysis')}>Open Analysis</Button>}>
              Project analysis writes these docs from the source: what the project is for, its components, how its main features run and how to run it.
            </EmptyState>
          ) : (
            <DocsContent docs={docs} section={current} path={route.params.get('path') ?? ''} n={Number(route.params.get('n') ?? 0)} />
          )}
        </div>
      </div>
    </div>
  );
}

function DocsContent({ docs, section, path, n }: { docs: ProjectDocs; section: DocsSection; path: string; n: number }) {
  return <DocRefsProvider docs={docs}><DocsSectionPage docs={docs} section={section} path={path} n={n} /></DocRefsProvider>;
}

function DocsSectionPage({ docs, section, path, n }: { docs: ProjectDocs; section: DocsSection; path: string; n: number }) {
  switch (section) {
    case 'start':
      return <StartPage docs={docs} />;
    case 'architecture':
      return <ArchitecturePage docs={docs} />;
    case 'components':
      return path ? <ComponentPage docs={docs} path={path} /> : <ComponentsIndex docs={docs} />;
    case 'flows':
      return n > 0 ? <WalkthroughPage docs={docs} n={n} /> : <WalkthroughsIndex docs={docs} />;
    case 'changes':
      return n > 0 ? <ChangeGuidePage docs={docs} n={n} /> : <ChangeGuideIndex docs={docs} />;
    case 'dependencies':
      return <DependenciesPage docs={docs} />;
    default:
      return <OverviewPage docs={docs} />;
  }
}

function DocsNav({ docs, current }: { docs: ProjectDocs | undefined; current: DocsSection }) {
  const route = useRoute();
  const { data } = useOverview();
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = useBusy();
  const [question, setQuestion] = useState('');
  const path = route.params.get('path') ?? '';
  const n = Number(route.params.get('n') ?? 0);
  const walkthroughs = docs?.walkthroughs ?? [];
  const changes = docs?.change_guide ?? [];
  const components = docs?.components ?? [];

  return (
    <nav className="docs-nav" aria-label="Docs">
      <form className="docs-ask" role="search" onSubmit={e => { e.preventDefault(); const text = question.trim(); if (text) { handQuestion(text); navigate('/docs/ask', { q: text }); } setQuestion(''); }}>
        <Input icon={MessageCircleQuestion} placeholder="Ask the code…" value={question} onChange={e => setQuestion(e.target.value)} aria-label="Ask the code a question" />
      </form>
      <div className="docs-nav-scroll">
        <NavLink to="/docs/overview" icon={Compass} active={current === 'overview'}>Overview</NavLink>
        <NavLink to="/docs/start" icon={Terminal} active={current === 'start'} count={docs?.commands?.length}>Get started</NavLink>
        <NavLink to="/docs/architecture" icon={Layers} active={current === 'architecture'}>Architecture</NavLink>
        <NavGroup title="How it works" icon={RouteIcon} to="/docs/flows" active={current === 'flows' && !n} count={walkthroughs.length}>
          {walkthroughs.map((walkthrough, index) => (
            <NavLink key={index} to="/docs/flows" params={{ n: String(index + 1) }} active={current === 'flows' && n === index + 1} nested>{walkthrough.title}</NavLink>
          ))}
        </NavGroup>
        <NavGroup title="Where to change" icon={Wrench} to="/docs/changes" active={current === 'changes' && !n} count={changes.length}>
          {changes.map((change, index) => (
            <NavLink key={index} to="/docs/changes" params={{ n: String(index + 1) }} active={current === 'changes' && n === index + 1} nested>{change.title}</NavLink>
          ))}
        </NavGroup>
        <NavGroup title="Components" icon={Boxes} to="/docs/components" active={current === 'components' && !path} count={components.length}>
          {components.map(component => (
            <NavLink key={component.path} to="/docs/components" params={{ path: component.path }} active={current === 'components' && path === component.path} nested mono title={component.summary}>
              <PathLabel path={componentLabel(component)} />
            </NavLink>
          ))}
        </NavGroup>
        <NavLink to="/docs/dependencies" icon={Library} active={current === 'dependencies'} count={docs?.dependencies?.length}>Dependencies</NavLink>
        <NavLink to="/docs/ask" icon={MessageCircleQuestion} active={current === 'ask'}>Ask the code</NavLink>
      </div>
      <div className="docs-refresh">
        <RefreshStatus {...latestRefresh(data?.docs_refresh, data?.analysis_run?.docs)} />
        {project && docs ? (
          <Button size="sm" variant="ghost" icon={RefreshCw} disabled={Boolean(busy) || data?.docs_refresh?.status === 'running'} onClick={() => void actions.refreshDocs(project, worktreePath)}
            title="Summarize again the components whose files changed, then write the overview again">
            Refresh docs
          </Button>
        ) : null}
      </div>
    </nav>
  );
}

/** A path with its parent directories dimmed, so the last part reads first. */
function PathLabel({ path }: { path: string }) {
  const slash = path.lastIndexOf('/');
  if (slash < 0) return <span className="truncate">{path}</span>;
  return <span className="truncate"><span className="faint">{path.slice(0, slash + 1)}</span>{path.slice(slash + 1)}</span>;
}

function NavLink({ to, params, icon: Icon, active, count, nested, mono, title, children }: {
  to: string; params?: Record<string, string>; icon?: typeof Compass; active: boolean; count?: number; nested?: boolean; mono?: boolean; title?: string; children: ReactNode;
}) {
  const href = '#' + to + (params ? '?' + new URLSearchParams(params).toString() : '');
  return (
    <a href={href} className={cx('docs-link', nested && 'is-nested', mono && 'mono', active && 'is-active')} aria-current={active ? 'page' : undefined} title={title}
      onClick={e => { e.preventDefault(); navigate(to, params); }}>
      {Icon ? <Icon size={14} aria-hidden /> : null}
      {typeof children === 'string' ? <span className="truncate">{children}</span> : children}
      {count ? <><span className="spacer" /><span className="xs faint">{count}</span></> : null}
    </a>
  );
}

function NavGroup({ title, icon: Icon, to, active, count, children }: { title: string; icon: typeof Compass; to: string; active: boolean; count: number; children: ReactNode }) {
  const [open, setOpen] = useState(true);
  return (
    <div className="docs-group">
      <div className="docs-group-head">
        <button type="button" className="docs-toggle" aria-expanded={open} aria-label={`${open ? 'Collapse' : 'Expand'} ${title}`} onClick={() => setOpen(!open)} disabled={!count}>
          <ChevronRight size={13} className={cx('chev', open && count > 0 && 'is-open')} aria-hidden />
        </button>
        <NavLink to={to} icon={Icon} active={active} count={count}>{title}</NavLink>
      </div>
      {open ? children : null}
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
