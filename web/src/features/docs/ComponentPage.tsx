// A component's page: what it does, how it connects, the walkthroughs that
// pass through it and its files with a line on each; and the list of all
// components.

import { Boxes, MessageCircleQuestion } from 'lucide-react';
import { useDocsComponent } from '@/api/hooks';
import type { DocsComponent, DocsComponentFiles, ProjectDocs } from '@/api/types';
import { componentLabel, firstSentence, walkthroughsThrough } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { basename, dirname, plural } from '@/lib/util';
import { Button, EmptyState, Skeleton } from '@/components/ui';
import { DocsStatusNote, EntryPoints, FileLink, ModelWritten } from './GuidePages';

export function ComponentsIndex({ docs }: { docs: ProjectDocs }) {
  const components = docs.components ?? [];
  return (
    <article className="doc">
      <h1>Components</h1>
      <p className="small muted">The directories that hold code, folded so at most sixteen show. Which one imports which is read from the code.</p>
      <ul className="component-list">
        {components.map(component => (
          <li key={component.path}>
            <button type="button" className="component-row" onClick={() => navigate('/docs/components', { path: component.path })}>
              <span className="mono small strong">{componentLabel(component)}</span>
              <span className="small muted truncate">{component.summary ? firstSentence(component.summary) : meta(component)}</span>
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
  const byPath = new Map(components.map(c => [c.path, c]));
  const chip = (target: string) => (
    <button key={target} type="button" className="chip mono" onClick={() => navigate('/docs/components', { path: target })} title={byPath.get(target)?.summary}>{componentLabel({ path: target })}</button>
  );
  const flows = walkthroughsThrough(component.path, docs.walkthroughs ?? [], components);
  return (
    <article className="doc">
      {component.summary_source === 'overview' ? <DocsStatusNote status={docs.status} /> : null}
      <header className="doc-head">
        <div className="xs muted"><button type="button" className="link-btn" onClick={() => navigate('/docs/components')}>Components</button> /</div>
        <h1 className="mono">{componentLabel(component)}</h1>
        <div className="xs muted">{meta(component)}</div>
      </header>
      {component.summary ? (
        <div className="stack">
          <p>{component.summary} <ModelWritten title={component.summary_source === 'module' ? 'Written by the analyze model from the summaries of its files.' : 'From the project overview, written by the analyze model.'} /></p>
          {component.responsibilities?.length ? <ul className="plain-list bulleted small">{component.responsibilities.map((r, i) => <li key={i}>{r}</li>)}</ul> : null}
        </div>
      ) : <p className="small muted">No summary yet: an analysis run writes one from its files' summaries.</p>}
      <dl className="relations">
        <div><dt>Imports</dt><dd>{component.imports?.length ? component.imports.map(chip) : <span className="small faint">nothing in this project</span>}</dd></div>
        <div><dt>Used by</dt><dd>{component.used_by?.length ? component.used_by.map(chip) : <span className="small faint">nothing in this project</span>}</dd></div>
        {component.entry_points?.length ? <div><dt>Starts at</dt><dd><EntryPoints entries={component.entry_points} /></dd></div> : null}
        {flows.length ? (
          <div><dt>Walkthroughs</dt><dd>{flows.map(({ n, walkthrough }) => <button key={n} type="button" className="chip" onClick={() => navigate('/docs/flows', { n: String(n) })}>{walkthrough.title}</button>)}</dd></div>
        ) : null}
      </dl>
      <div className="row">
        <h2 className="grow">Files</h2>
        <Button size="sm" variant="ghost" icon={MessageCircleQuestion} onClick={() => navigate('/docs/ask', { q: `How does ${componentLabel(component)} work, and where should I start reading it?` })}>Ask about it</Button>
      </div>
      <ComponentFiles path={component.path} />
    </article>
  );
}

function ComponentFiles({ path }: { path: string }) {
  const { data, isLoading, error } = useDocsComponent(path);
  if (isLoading) return <Skeleton lines={6} />;
  if (error || !data) return <p className="small muted">{error instanceof Error ? error.message : 'The files could not be read.'}</p>;
  if (!data.files.length) return <p className="small muted">No code files.</p>;
  const groups = groupByDirectory(data);
  const summarized = data.files.filter(file => file.summary).length;
  return (
    <div className="stack">
      {groups.map(([dir, files]) => (
        <section key={dir} className="file-group" aria-label={`Files in ${dir || 'the project root'}`}>
          {groups.length > 1 || dir !== path ? <div className="xs muted mono">{dir ? `${dir}/` : './'}</div> : null}
          <ul className="file-list">
            {files.map(file => (
              <li key={file.path}>
                <FileLink path={file.path} label={basename(file.path)} />
                <span className="small muted file-summary">{file.summary ?? ''}</span>
                <span className="xs faint">{file.lines}</span>
              </li>
            ))}
          </ul>
        </section>
      ))}
      <div className="xs faint">
        {summarized ? `One line per file from its summary in the last analysis (AI); ${summarized} of ${data.files.length} have one.` : 'No file has a summary yet: an analysis writes them.'}
      </div>
    </div>
  );
}

function groupByDirectory(data: DocsComponentFiles): [string, DocsComponentFiles['files']][] {
  const groups = new Map<string, DocsComponentFiles['files']>();
  for (const file of data.files) groups.set(dirname(file.path), [...(groups.get(dirname(file.path)) ?? []), file]);
  return [...groups.entries()];
}
