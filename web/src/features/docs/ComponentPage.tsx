// A component's page: what it does, what uses it and what it imports, the
// guides that pass through it, the functions to start reading from and its
// files (code before tests, the longest first) with a line on each; and the
// list of all components.

import { ArrowRight, Boxes, MessageCircleQuestion } from 'lucide-react';
import { useDocsComponent } from '@/api/hooks';
import type { DocsComponent, DocsComponentFiles, ProjectDocs } from '@/api/types';
import { architectureRoles, componentLabel, firstSentence, guideStops, walkthroughsThrough } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { basename, dirname, isTestPath, plural } from '@/lib/util';
import { Button, EmptyState, Skeleton } from '@/components/ui';
import { DocText } from './DocText';
import { DocTitle, DocsStatusNote, EntryPoints, FileLink, SectionHead } from './GuidePages';

export function ComponentsIndex({ docs }: { docs: ProjectDocs }) {
  const components = docs.components ?? [];
  const roles = architectureRoles(docs.overview?.architecture, components);
  return (
    <article className="doc">
      <header className="doc-head">
        <DocTitle ai={components.some(component => component.summary)}>Components</DocTitle>
        <p className="small muted">Each is a directory of code. The diagram of how they connect is on Architecture.</p>
      </header>
      <ul className="component-list">
        {components.map(component => (
          <li key={component.path}>
            <button type="button" className="component-row" onClick={() => navigate('/docs/components', { path: component.path })}>
              <span className="mono small strong">{componentLabel(component)}</span>
              <span className="small muted">{firstSentence(component.summary ?? roles.get(component.path), 200) || (component.languages ?? []).join(', ')}</span>
              <span className="xs faint component-size">{plural(component.files ?? 0, 'file')} · {(component.lines ?? 0).toLocaleString()} lines</span>
            </button>
          </li>
        ))}
      </ul>
    </article>
  );
}

function meta(component: DocsComponent): string {
  return [plural(component.files ?? 0, 'file'), component.lines ? `${component.lines.toLocaleString()} lines` : '', (component.languages ?? []).join(', ')].filter(Boolean).join(' · ');
}

export function ComponentPage({ docs, path }: { docs: ProjectDocs; path: string }) {
  const components = docs.components ?? [];
  const component = components.find(c => c.path === path);
  if (!component) {
    return (
      <EmptyState icon={Boxes} title="No such component" action={<Button onClick={() => navigate('/docs/components')}>All components</Button>}>
        The docs show no component at {path} any more; the code may have moved.
      </EmptyState>
    );
  }
  const walkthroughs = docs.walkthroughs ?? [];
  const changes = docs.change_guide ?? [];
  const flows = walkthroughsThrough(component.path, walkthroughs, components);
  const changed = walkthroughsThrough(component.path, changes, components);
  const stops = guideStops(component.path, walkthroughs, changes, components);
  return (
    <article className="doc">
      {component.summary_source === 'overview' ? <DocsStatusNote status={docs.status} /> : null}
      <header className="doc-head">
        <div className="xs muted"><button type="button" className="link-btn" onClick={() => navigate('/docs/components')}>Components</button> /</div>
        <DocTitle mono ai={Boolean(component.summary)} title={component.summary_source === 'module' ? 'Written by the analyze model from the summaries of its files.' : 'From the project overview, written by the analyze model.'}>{componentLabel(component)}</DocTitle>
        <div className="xs muted">{meta(component)}</div>
      </header>
      {component.summary ? (
        <div className="stack">
          <p className="lead"><DocText text={component.summary} /></p>
          {component.responsibilities?.length ? <ul className="plain-list bulleted small">{component.responsibilities.map((r, i) => <li key={i}><DocText text={r} /></li>)}</ul> : null}
        </div>
      ) : <p className="small muted">No summary yet.</p>}
      <Neighbours component={component} components={components} />
      {component.entry_points?.length || flows.length || changed.length ? (
        <dl className="relations">
          {component.entry_points?.length ? <div><dt>Starts at</dt><dd><EntryPoints entries={component.entry_points} /></dd></div> : null}
          {flows.length ? (
            <div><dt>Walkthroughs</dt><dd>{flows.map(({ n, walkthrough }) => <button key={n} type="button" className="chip" onClick={() => navigate('/docs/flows', { n: String(n) })}>{walkthrough.title}</button>)}</dd></div>
          ) : null}
          {changed.length ? (
            <div><dt>Changed in</dt><dd>{changed.map(({ n, walkthrough }) => <button key={n} type="button" className="chip" onClick={() => navigate('/docs/changes', { n: String(n) })}>{walkthrough.title}</button>)}</dd></div>
          ) : null}
        </dl>
      ) : null}
      {stops.length ? (
        <section aria-label="Start reading here">
          <SectionHead title="Start reading here" note="the functions the guides stop at, most visited first" />
          <ul className="stop-list">
            {stops.slice(0, STOPS_SHOWN).map(stop => (
              <li key={`${stop.path}#${stop.symbol}`}>
                <button type="button" className="link-btn mono stop-name" title={`Open ${stop.path} at line ${stop.line} in Code`} onClick={() => navigate('/code', { file: stop.path, line: String(stop.line) })}>{stop.symbol}</button>
                <span className="small muted stop-text"><DocText text={stop.text} /></span>
                <span className="xs faint mono">{basename(stop.path)}:{stop.line}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      <section aria-label="Files">
        <SectionHead title="Files">
          <Button size="sm" variant="ghost" icon={MessageCircleQuestion} onClick={() => navigate('/docs/ask', { q: `How does ${componentLabel(component)} work, and where should I start reading it?` })}>Ask about it</Button>
        </SectionHead>
        <ComponentFiles path={component.path} />
      </section>
    </article>
  );
}

/** The most functions a component page lists to start reading from. */
const STOPS_SHOWN = 8;

/** What uses the component and what it imports, either side of it, as the code has it. */
function Neighbours({ component, components }: { component: DocsComponent; components: DocsComponent[] }) {
  const byPath = new Map(components.map(c => [c.path, c]));
  const chip = (target: string) => (
    <button key={target} type="button" className="chip mono" onClick={() => navigate('/docs/components', { path: target })} title={byPath.get(target)?.summary}>{componentLabel({ path: target })}</button>
  );
  const none = <span className="small faint">nothing in this project</span>;
  return (
    <div className="neighbours" role="group" aria-label={`What uses ${componentLabel(component)} and what it imports`}>
      <div className="neighbours-side">
        <span className="label-row">Used by</span>
        <div className="neighbours-chips">{component.used_by?.length ? component.used_by.map(chip) : none}</div>
      </div>
      <ArrowRight size={16} className="faint" aria-hidden />
      <div className="neighbours-self mono">{componentLabel(component)}</div>
      <ArrowRight size={16} className="faint" aria-hidden />
      <div className="neighbours-side">
        <span className="label-row">Imports</span>
        <div className="neighbours-chips">{component.imports?.length ? component.imports.map(chip) : none}</div>
      </div>
    </div>
  );
}

function ComponentFiles({ path }: { path: string }) {
  const { data, isLoading, error } = useDocsComponent(path);
  if (isLoading) return <Skeleton lines={6} />;
  if (error || !data) return <p className="small muted">{error instanceof Error ? error.message : 'The files could not be read.'}</p>;
  if (!data.files.length) return <p className="small muted">No code files.</p>;
  const code = data.files.filter(file => !isTestPath(file.path));
  const tests = data.files.filter(file => isTestPath(file.path));
  return (
    <div className="stack">
      {code.length ? <FileGroups files={code} path={path} /> : <p className="small muted">Only tests.</p>}
      {tests.length ? (
        <details className="doc-fold">
          <summary>{plural(tests.length, 'test file')} <span className="xs faint">{tests.reduce((sum, file) => sum + file.lines, 0).toLocaleString()} lines</span></summary>
          <FileGroups files={tests} path={path} />
        </details>
      ) : null}
    </div>
  );
}

/** Files by directory, the longest first: in most code the longest file is where the work is. */
function FileGroups({ files, path }: { files: DocsComponentFiles['files']; path: string }) {
  const groups = groupByDirectory(files);
  return (
    <>
      {groups.map(([dir, group]) => (
        <section key={dir} className="file-group" aria-label={`Files in ${dir || 'the project root'}`}>
          {groups.length > 1 || dir !== path ? <div className="xs muted mono">{dir ? `${dir}/` : './'}</div> : null}
          <ul className="file-list">
            {group.map(file => (
              <li key={file.path}>
                <FileLink path={file.path} label={basename(file.path)} />
                <span className="small muted file-summary">{file.summary ? <DocText text={file.summary} /> : ''}</span>
                <span className="xs faint" title={`${file.lines.toLocaleString()} lines`}>{file.lines.toLocaleString()}</span>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </>
  );
}

function groupByDirectory(files: DocsComponentFiles['files']): [string, DocsComponentFiles['files']][] {
  const groups = new Map<string, DocsComponentFiles['files']>();
  for (const file of files) groups.set(dirname(file.path), [...(groups.get(dirname(file.path)) ?? []), file]);
  return [...groups.entries()].map(([dir, group]) => [dir, [...group].sort((a, b) => b.lines - a.lines || a.path.localeCompare(b.path))]);
}
