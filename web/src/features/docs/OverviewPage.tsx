// The docs' front page, in the order a new maintainer needs it: what the
// project is, how it is built (the architecture diagram), what it does (the
// walkthroughs), where the usual changes go, and how to run it.

import { useMemo, type ReactNode } from 'react';
import { useProject } from '@/api/hooks';
import type { ProjectDocs } from '@/api/types';
import { architectureRoles } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { ComponentGraph } from './ComponentGraph';
import { DocText } from './DocText';
import { CommandList, DocTitle, DocsStatusNote, FileLink, PageLink, SectionHead } from './GuidePages';
import { CHANGE_GUIDE, GuideCards, WALKTHROUGHS } from './WalkthroughPage';

/** The most commands the front page lists before it points at Get started. */
const COMMANDS_SHOWN = 4;

export function OverviewPage({ docs }: { docs: ProjectDocs }) {
  const project = useProject().data;
  const overview = docs.overview;
  const components = useMemo(() => docs.components ?? [], [docs.components]);
  const roles = useMemo(() => architectureRoles(overview?.architecture, components), [overview?.architecture, components]);
  const walkthroughs = docs.walkthroughs ?? [];
  const changes = docs.change_guide ?? [];
  const commands = docs.commands ?? [];
  return (
    <article className="doc doc-front">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <DocTitle ai={Boolean(overview?.purpose)}>{project?.name ?? 'Overview'}</DocTitle>
        {overview?.purpose ? <p className="lead"><DocText text={overview.purpose} /></p> : <p className="muted">No overview yet.</p>}
      </header>
      <ProjectFacts docs={docs} />
      <section aria-label="How it is built">
        <SectionHead title="How it is built" note={`${plural(components.length, 'component')}, importers above what they import`}>
          <PageLink to="/docs/architecture">Architecture</PageLink>
        </SectionHead>
        <ComponentGraph components={components} graph={docs.graph} roles={roles} onOpen={component => navigate('/docs/components', { path: component.path })} />
      </section>
      <section aria-label="What it does">
        <SectionHead title="What it does" note={walkthroughs.length ? `${plural(walkthroughs.length, 'walkthrough')}, each followed through the code` : undefined} />
        <GuideCards docs={docs} kind={{ ...WALKTHROUGHS, missing: 'Comes with the next overview.' }} columns />
      </section>
      <section aria-label="Where to change it">
        <SectionHead title="Where to change it" note={changes.length ? 'the usual changes, as the files and functions to touch' : undefined} />
        <GuideCards docs={docs} kind={{ ...CHANGE_GUIDE, missing: 'Comes with the next overview.' }} columns />
      </section>
      <section aria-label="Run it">
        <SectionHead title="Run it" note={commands.length ? "declared in the project's files" : undefined}>
          {commands.length > COMMANDS_SHOWN ? <PageLink to="/docs/start">All {commands.length} commands</PageLink> : null}
        </SectionHead>
        {commands.length ? <CommandList commands={commands.slice(0, COMMANDS_SHOWN)} showDir /> : <p className="small muted">No test, lint or run command was found in the project's files.</p>}
      </section>
    </article>
  );
}

/** The project in numbers and where it starts, read from the code. */
function ProjectFacts({ docs }: { docs: ProjectDocs }) {
  const overview = docs.overview;
  const languages = (overview?.languages ?? []).slice(0, 3);
  const entries = (docs.graph?.entry_points ?? []).slice(0, 3);
  const facts: { label: string; value: ReactNode }[] = [
    { label: languages.length === 1 ? 'Language' : 'Languages', value: languages.map(language => (languages.length > 1 ? `${language.name} ${language.files}` : language.name)).join(' · ') },
    { label: 'Size', value: [overview?.lines ? `${overview.lines.toLocaleString()} lines` : '', overview?.files ? plural(overview.files, 'file') : ''].filter(Boolean).join(' in ') },
    { label: 'Components', value: String((docs.components ?? []).length) },
    { label: 'Starts at', value: entries.length ? <span className="row-wrap">{entries.map(entry => <FileLink key={entry.path} path={entry.path} />)}</span> : '' },
  ].filter(fact => fact.value);
  if (!facts.length) return null;
  return (
    <dl className="doc-facts" aria-label="The project in numbers">
      {facts.map(fact => (
        <div key={fact.label}>
          <dt>{fact.label}</dt>
          <dd>{fact.value}</dd>
        </div>
      ))}
    </dl>
  );
}
