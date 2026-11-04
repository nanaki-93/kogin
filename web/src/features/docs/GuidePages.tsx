// The guide pages of the docs: overview, get started, architecture and
// dependencies, with the pieces the other docs pages share.

import type { ReactNode } from 'react';
import { ArrowRight, Copy, FileCode2, Sparkles } from 'lucide-react';
import { useProject } from '@/api/hooks';
import type { DocsCommand, DocsEntryPoint, ProjectDocs } from '@/api/types';
import { architectureLines, componentLabel, firstSentence } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge, Callout, IconButton } from '@/components/ui';
import { useFeedback } from '@/components/overlays';
import { ComponentGraph } from './ComponentGraph';
import { DocText } from './DocText';

/** Marks a page whose prose a model wrote, once in its header, so it is never read as a fact from the code. */
export function ModelWritten({ title = 'Written by the analyze model from the code. Check the code before relying on it.' }: { title?: string }) {
  return <Badge tone="accent" icon={Sparkles} title={title}>AI</Badge>;
}

/** A docs page's title, with the AI mark when the page shows model-written prose. */
export function DocTitle({ children, ai, title, mono }: { children: ReactNode; ai?: boolean; title?: string; mono?: boolean }) {
  return (
    <div className="row doc-title">
      <h1 className={mono ? 'mono grow' : 'grow'}>{children}</h1>
      {ai ? <ModelWritten title={title} /> : null}
    </div>
  );
}

const STATUS_NOTES: Record<string, { title: string; body: string }> = {
  unavailable: { title: 'No overview yet', body: 'What the project is for and how it works are written by the analyze model. Run an analysis or Refresh docs to write them; the components and commands are read from the code.' },
  failed: { title: 'The overview could not be written', body: 'The analyze model did not return a usable overview. The components and commands are read from the code.' },
  stale: { title: 'The overview is out of date', body: 'It was written for an older version of the code. Refresh docs to write it again; the components and commands are current.' },
};

/** Says when the model-written parts are missing or out of date. */
export function DocsStatusNote({ status }: { status: string }) {
  const note = STATUS_NOTES[status];
  if (!note) return null;
  return <div className="docs-notice"><Callout tone={status === 'unavailable' ? 'neutral' : 'warning'} title={note.title}>{note.body}</Callout></div>;
}

/** A file, at a line when known, that opens in Code. */
export function FileLink({ path, line, label }: { path: string; line?: number; label?: string }) {
  return (
    <button type="button" className="link-btn mono xs file-link" onClick={() => navigate('/code', { file: path, line: line ? String(line) : undefined })} title={`Open ${path}${line ? ` at line ${line}` : ''} in Code`}>
      <FileCode2 size={12} aria-hidden />
      {label ?? path}{line ? `:${line}` : ''}
    </button>
  );
}

function entryLabel(entry: DocsEntryPoint): string {
  if (entry.kind === 'main') return entry.detail ? `${entry.detail} main` : 'main';
  if (entry.kind === 'script') return `script ${entry.detail ?? ''}`.trim();
  return entry.detail ? `${entry.kind} ${entry.detail}` : entry.kind;
}

export function EntryPoints({ entries }: { entries: DocsEntryPoint[] }) {
  if (!entries.length) return <p className="small muted">No entry point was found in the code.</p>;
  return (
    <ul className="plain-list">
      {entries.map((entry, index) => (
        <li key={index}><FileLink path={entry.path} /> <span className="xs muted">{entryLabel(entry)}</span></li>
      ))}
    </ul>
  );
}

function PageLink({ to, params, children }: { to: string; params?: Record<string, string>; children: ReactNode }) {
  return (
    <button type="button" className="link-btn small page-link" onClick={() => navigate(to, params)}>
      {children} <ArrowRight size={13} aria-hidden />
    </button>
  );
}

export function OverviewPage({ docs }: { docs: ProjectDocs }) {
  const project = useProject().data;
  const overview = docs.overview;
  const languages = overview?.languages ?? [];
  const components = docs.components ?? [];
  const walkthroughs = docs.walkthroughs ?? [];
  const facts = [
    languages.slice(0, 3).map(language => `${language.name} ${language.files}`).join(' · '),
    overview?.files ? plural(overview.files, 'file') : '',
    overview?.lines ? `${overview.lines.toLocaleString()} lines` : '',
    plural(components.length, 'component'),
  ].filter(Boolean);
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <DocTitle ai={Boolean(overview?.purpose)}>{project?.name ?? 'Overview'}</DocTitle>
        <div className="xs muted">{facts.join(' · ')}</div>
      </header>
      {overview?.purpose ? (
        <p className="lead"><DocText text={overview.purpose} /></p>
      ) : (
        <p className="muted">No overview yet.</p>
      )}
      <div className="start-grid">
        <section className="doc-card" aria-label="Where it starts">
          <h2>Where it starts</h2>
          <EntryPoints entries={(docs.graph?.entry_points ?? []).slice(0, 5)} />
        </section>
        <section className="doc-card" aria-label="How it works">
          <h2>How it works</h2>
          <GuideTitles guides={walkthroughs} to="/docs/flows" empty="Comes with the next overview." />
        </section>
        <section className="doc-card" aria-label="Where to change">
          <h2>Where to change</h2>
          <GuideTitles guides={docs.change_guide ?? []} to="/docs/changes" empty="Comes with the next overview." />
        </section>
        <section className="doc-card" aria-label="Run it">
          <h2>Run it</h2>
          {docs.commands?.length ? (
            <>
              <ul className="plain-list">{docs.commands.slice(0, 3).map(command => <li key={command.command + command.dir}><code className="command">{command.command}</code>{command.dir ? <span className="xs muted"> in {command.dir}/</span> : null}</li>)}</ul>
              <PageLink to="/docs/start">All commands</PageLink>
            </>
          ) : <p className="small muted">No test, lint or run command was found in the project's files.</p>}
        </section>
      </div>
      <section aria-label="Components">
        <div className="row"><h2 className="grow">Components</h2><PageLink to="/docs/architecture">See how they connect</PageLink></div>
        <ul className="component-list">
          {components.map(component => (
            <li key={component.path}>
              <button type="button" className="component-row" onClick={() => navigate('/docs/components', { path: component.path })}>
                <span className="mono small strong">{componentLabel(component)}</span>
                <span className="small muted truncate">{component.summary ? firstSentence(component.summary) : `${plural(component.files ?? 0, 'file')}${component.languages?.length ? ` · ${component.languages[0]}` : ''}`}</span>
              </button>
            </li>
          ))}
        </ul>
      </section>
    </article>
  );
}

export function StartPage({ docs }: { docs: ProjectDocs }) {
  const { toast } = useFeedback();
  const groups = new Map<string, DocsCommand[]>();
  for (const command of docs.commands ?? []) groups.set(command.dir ?? '', [...(groups.get(command.dir ?? '') ?? []), command]);
  return (
    <article className="doc">
      <h1>Get started</h1>
      <p className="small muted">Read from the project's files: each subproject's test and lint commands and the package scripts that build or start it.</p>
      {groups.size ? [...groups.entries()].map(([dir, commands]) => (
        <section key={dir} aria-label={dir ? `Commands in ${dir}` : 'Commands in the project root'}>
          <h2>{dir ? <span className="mono">{dir}/</span> : 'Project root'}</h2>
          <div className="command-list">
            {commands.map(command => (
              <div key={command.label + command.command} className="command-row">
                <span className="command-label small">{command.label}</span>
                <code className="command">{command.command}</code>
                <span className="spacer" />
                <span className="xs faint">{command.source}</span>
                <IconButton icon={Copy} label={`Copy ${command.command}`} size="sm" onClick={() => { void navigator.clipboard?.writeText(command.command); toast('Command copied.', 'success'); }} />
              </div>
            ))}
          </div>
        </section>
      )) : <p className="muted">No test, lint or run command was found in the project's files.</p>}
      <section aria-label="Where it starts">
        <h2>Where it starts</h2>
        <EntryPoints entries={docs.graph?.entry_points ?? []} />
      </section>
    </article>
  );
}

/** The titles of walkthroughs or change-guide entries, each opening its page. */
function GuideTitles({ guides, to, empty }: { guides: { title: string }[]; to: string; empty: string }) {
  if (!guides.length) return <p className="small muted">{empty}</p>;
  return (
    <ol className="plain-list numbered">
      {guides.map((guide, index) => (
        <li key={index}><button type="button" className="link-btn" onClick={() => navigate(to, { n: String(index + 1) })}>{guide.title}</button></li>
      ))}
    </ol>
  );
}

export function ArchitecturePage({ docs }: { docs: ProjectDocs }) {
  const lines = architectureLines(docs.overview?.architecture);
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <DocTitle ai={lines.length > 0}>Architecture</DocTitle>
      {lines.length > 1 ? (
        <ul className="plain-list bulleted">{lines.map((line, index) => <li key={index}><DocText text={line} /></li>)}</ul>
      ) : lines.length ? <p><DocText text={lines[0]} /></p> : null}
      <ComponentGraph components={docs.components ?? []} graph={docs.graph} onOpen={component => navigate('/docs/components', { path: component.path })} />
    </article>
  );
}

export function DependenciesPage({ docs }: { docs: ProjectDocs }) {
  const bySource = new Map<string, NonNullable<ProjectDocs['dependencies']>>();
  for (const dependency of docs.dependencies ?? []) bySource.set(dependency.source, [...(bySource.get(dependency.source) ?? []), dependency]);
  return (
    <article className="doc">
      <h1>Dependencies</h1>
      <p className="small muted">What the project's manifests declare.</p>
      {bySource.size ? [...bySource.entries()].map(([source, dependencies]) => (
        <section key={source} aria-label={`Declared in ${source}`}>
          <h2><span className="mono">{source}</span> <span className="xs muted">{dependencies[0].kind}</span></h2>
          <table className="table dependency-table">
            <thead><tr><th>Name</th><th>Version</th></tr></thead>
            <tbody>
              {dependencies.map(dependency => (
                <tr key={dependency.name}>
                  <td className="mono small">{dependency.name}{dependency.dev ? <> <Badge>dev</Badge></> : null}</td>
                  <td className="mono xs muted">{dependency.version}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )) : <p className="muted">No manifest declares dependencies.</p>}
    </article>
  );
}
