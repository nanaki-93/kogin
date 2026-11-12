// The guide pages of the docs: get started, architecture and dependencies,
// with the pieces the other docs pages share.

import { useMemo, useState, type ReactNode } from 'react';
import { ArrowRight, ChevronLeft, ChevronRight, Compass, Copy, FileCode2, Layers, Route as RouteIcon, ShieldCheck, Sparkles } from 'lucide-react';
import { useProjectRules } from '@/api/hooks';
import type { DocsCommand, DocsEntryPoint, ProjectDocs, Walkthrough } from '@/api/types';
import { analyzeArchitecture } from '@/lib/architectureLens';
import { architectureParts, componentLabel, componentOf, componentTrail, walkthroughsThrough } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge, Button, Callout, IconButton, Segmented, cx } from '@/components/ui';
import { useFeedback } from '@/components/overlays';
import { ComponentGraph, type GraphLens } from './ComponentGraph';
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

export function PageLink({ to, params, children }: { to: string; params?: Record<string, string>; children: ReactNode }) {
  return (
    <button type="button" className="link-btn small page-link" onClick={() => navigate(to, params)}>
      {children} <ArrowRight size={13} aria-hidden />
    </button>
  );
}

/** A section's heading inside a docs page, with a quiet note on what it holds and a link to the page with the rest. */
export function SectionHead({ title, note, children }: { title: string; note?: ReactNode; children?: ReactNode }) {
  return (
    <div className="doc-section-head">
      <h2>{title}</h2>
      {note ? <span className="small muted">{note}</span> : null}
      <span className="spacer" />
      {children}
    </div>
  );
}

/** The components a guide passes through, in order; each opens its page unless the trail sits inside a link. */
export function ComponentTrail({ trail, links }: { trail: string[]; links?: boolean }) {
  if (!trail.length) return null;
  return (
    <span className="trail" aria-label={`Passes through ${trail.map(path => componentLabel({ path })).join(', then ')}`}>
      {trail.map((path, index) => (
        <span key={index} className="trail-stop">
          {index ? <ChevronRight size={12} className="faint" aria-hidden /> : null}
          {links ? (
            <button type="button" className="chip mono" onClick={() => navigate('/docs/components', { path })}>{trailLabel(path)}</button>
          ) : <span className="chip mono">{trailLabel(path)}</span>}
        </span>
      ))}
    </span>
  );
}

function trailLabel(path: string): string {
  return path === '.' ? 'root' : path;
}

/** The declared commands, each with where it is declared and a copy button. */
export function CommandList({ commands, showDir }: { commands: DocsCommand[]; showDir?: boolean }) {
  const { toast } = useFeedback();
  return (
    <div className="command-list">
      {commands.map(command => (
        <div key={command.label + command.command + (command.dir ?? '')} className="command-row">
          <span className="command-label small">{command.label}</span>
          <code className="command">{command.command}</code>
          {showDir && command.dir ? <span className="xs muted">in {command.dir}/</span> : null}
          <span className="spacer" />
          <span className="xs faint">{command.source}</span>
          <IconButton icon={Copy} label={`Copy ${command.command}`} size="sm" onClick={() => { void navigator.clipboard?.writeText(command.command); toast('Command copied.', 'success'); }} />
        </div>
      ))}
    </div>
  );
}

export function StartPage({ docs }: { docs: ProjectDocs }) {
  const groups = new Map<string, DocsCommand[]>();
  for (const command of docs.commands ?? []) groups.set(command.dir ?? '', [...(groups.get(command.dir ?? '') ?? []), command]);
  return (
    <article className="doc">
      <header className="doc-head">
        <h1>Get started</h1>
        <p className="small muted">Read from the project's files: each subproject's test and lint commands and the package scripts that build or start it.</p>
      </header>
      {groups.size ? [...groups.entries()].map(([dir, commands]) => (
        <section key={dir} aria-label={dir ? `Commands in ${dir}` : 'Commands in the project root'}>
          <SectionHead title={dir ? `${dir}/` : 'Project root'} />
          <CommandList commands={commands} />
        </section>
      )) : <p className="muted">No test, lint or run command was found in the project's files.</p>}
      <section aria-label="Where it starts">
        <SectionHead title="Where it starts" />
        <EntryPoints entries={docs.graph?.entry_points ?? []} />
      </section>
    </article>
  );
}

/** How the project is built: the diagram, interactive coupling & boundary lenses, flow stepper, then each part's size and ties. */
export function ArchitecturePage({ docs }: { docs: ProjectDocs }) {
  const components = useMemo(() => docs.components ?? [], [docs.components]);
  const rules = useProjectRules().data;
  const [lens, setLens] = useState<GraphLens>('topology');
  const [inspected, setInspected] = useState<string>('');
  const [flowKey, setFlowKey] = useState<string>('');
  const [stepIdx, setStepIdx] = useState(0);

  const { roles, rest } = useMemo(() => architectureParts(docs.overview?.architecture, components), [docs.overview?.architecture, components]);
  const analysis = useMemo(
    () => analyzeArchitecture(components, docs.graph, rules?.imports ?? []),
    [components, docs.graph, rules?.imports],
  );

  const allFlows = useMemo(() => {
    const out: { key: string; kind: 'Walkthrough' | 'Change guide'; guide: Walkthrough }[] = [];
    (docs.walkthroughs ?? []).forEach((guide, idx) => out.push({ key: `w:${idx}`, kind: 'Walkthrough', guide }));
    (docs.change_guide ?? []).forEach((guide, idx) => out.push({ key: `c:${idx}`, kind: 'Change guide', guide }));
    return out;
  }, [docs.change_guide, docs.walkthroughs]);

  const activeFlow = allFlows.find(f => f.key === flowKey);
  const activeStep = activeFlow?.guide.steps[stepIdx];
  const activeStepComponent = activeStep ? componentOf(activeStep.path, components) : undefined;
  const activeTrail = useMemo(
    () => (activeFlow ? componentTrail(activeFlow.guide, components) : []),
    [activeFlow, components],
  );

  const inspectedMetric = inspected ? analysis.byPath.get(inspected) : undefined;
  const most = Math.max(1, ...components.map(component => component.lines ?? 0));

  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <DocTitle ai={Boolean(docs.overview?.architecture) || components.some(component => component.summary)}>Architecture</DocTitle>
        <p className="small muted">{plural(components.length, 'component')}, importers above what they import. Open one for its files and the code the guides pass through.</p>
      </header>

      {components.length > 1 ? (
        <div className="arch-studio-bar" role="region" aria-label="Architecture lenses">
          <Segmented
            label="Architecture lens"
            value={lens}
            onChange={setLens}
            options={[
              { value: 'topology', label: 'Topology', icon: Layers },
              { value: 'coupling', label: 'Coupling (I)', icon: Compass },
              { value: 'boundaries', label: 'Boundaries', icon: ShieldCheck },
            ]}
          />
          <div className="chips arch-inspect-chips" role="group" aria-label="Inspect component">
            <span className="xs faint">Inspect:</span>
            {components.map(comp => (
              <button
                key={comp.path}
                type="button"
                className={cx('chip', 'mono', inspected === comp.path && 'is-on')}
                aria-pressed={inspected === comp.path}
                onClick={() => setInspected(prev => (prev === comp.path ? '' : comp.path))}
              >
                {componentLabel(comp)}
              </button>
            ))}
          </div>
        </div>
      ) : null}

      <ComponentGraph
        components={components}
        graph={docs.graph}
        roles={roles}
        lens={lens}
        metrics={analysis.byPath}
        inversions={analysis.inversions}
        focus={activeStepComponent ?? (inspected || undefined)}
        activeTrail={activeTrail}
        onOpen={component => navigate('/docs/components', { path: component.path })}
      />

      {inspectedMetric ? (
        <section className="arch-inspector-card" aria-label="Component coupling inspector">
          <div className="row">
            <span className="mono strong small">{componentLabel({ path: inspectedMetric.path })}</span>
            <Badge tone="accent">{inspectedMetric.roleLabel}</Badge>
            {inspectedMetric.instability !== null ? (
              <Badge tone="neutral" title="Martin Instability I = Ce / (Ca + Ce)">
                I = {inspectedMetric.instability.toFixed(2)}
              </Badge>
            ) : null}
            <span className="spacer" />
            <Button size="sm" variant="ghost" icon={Compass} onClick={() => navigate('/docs/atlas', { concept: 'acyclic-dependencies-instability' })}>
              Instability (I) in Atlas
            </Button>
            <Button size="sm" variant="secondary" onClick={() => navigate('/docs/components', { path: inspectedMetric.path })}>
              Open component
            </Button>
          </div>
          <div className="arch-metrics-grid xs">
            <div>
              <span className="faint">Fan-in (Ca): </span>
              <span className="strong">{plural(inspectedMetric.ca, 'caller')}</span>
              {inspectedMetric.transitiveCallers.length > inspectedMetric.ca
                ? ` (${inspectedMetric.transitiveCallers.length} transitive)`
                : ''}
            </div>
            <div>
              <span className="faint">Fan-out (Ce): </span>
              <span className="strong">{plural(inspectedMetric.ce, 'import')}</span>
              {inspectedMetric.transitiveDependencies.length > inspectedMetric.ce
                ? ` (${inspectedMetric.transitiveDependencies.length} transitive)`
                : ''}
            </div>
            <div>
              <span className="faint">Contract blast radius: </span>
              {inspectedMetric.transitiveCallers.length
                ? inspectedMetric.transitiveCallers.map(p => componentLabel({ path: p })).join(', ')
                : 'none (top-level or standalone)'}
            </div>
          </div>
          {inspectedMetric.forbiddenImports.length ? (
            <div className="xs arch-rules-row">
              <span className="strong">AGENTS.md boundary rules (never import): </span>
              {inspectedMetric.forbiddenImports.map(rule => (
                <code key={`${rule.never}@${rule.source}`} className="arch-rule-chip">{rule.never}</code>
              ))}
            </div>
          ) : (
            <div className="xs faint">No explicit never-import rules declared for this directory in AGENTS.md.</div>
          )}
        </section>
      ) : null}

      {allFlows.length ? (
        <section className="arch-flow-stepper" aria-label="Execution flow stepper">
          <div className="row-wrap">
            <RouteIcon size={14} className="tone-accent" aria-hidden />
            <span className="strong small">Trace execution flow</span>
            <span className="xs muted">Step through a real path live across the component graph:</span>
          </div>
          <div className="chips">
            {allFlows.map(flow => (
              <button
                key={flow.key}
                type="button"
                className={cx('chip', flowKey === flow.key && 'is-on')}
                aria-pressed={flowKey === flow.key}
                onClick={() => {
                  if (flowKey === flow.key) {
                    setFlowKey('');
                  } else {
                    setFlowKey(flow.key);
                    setStepIdx(0);
                  }
                }}
              >
                {flow.guide.title}
              </button>
            ))}
          </div>
          {activeFlow && activeStep ? (
            <div className="arch-flow-stage">
              <div className="row">
                <Badge tone="info">{activeFlow.kind} · Step {stepIdx + 1} of {activeFlow.guide.steps.length}</Badge>
                {activeStepComponent ? <span className="mono xs muted">in {componentLabel({ path: activeStepComponent })}</span> : null}
                <span className="spacer" />
                <IconButton
                  icon={ChevronLeft}
                  label="Previous step"
                  size="sm"
                  disabled={stepIdx === 0}
                  onClick={() => setStepIdx(i => Math.max(0, i - 1))}
                />
                <IconButton
                  icon={ChevronRight}
                  label="Next step"
                  size="sm"
                  disabled={stepIdx >= activeFlow.guide.steps.length - 1}
                  onClick={() => setStepIdx(i => Math.min(activeFlow.guide.steps.length - 1, i + 1))}
                />
              </div>
              <p className="small"><DocText text={activeStep.text} /></p>
              <div className="row xs">
                <FileLink path={activeStep.path} line={activeStep.line} />
                {activeStep.symbol ? <code className="muted">{activeStep.symbol}</code> : null}
              </div>
            </div>
          ) : null}
        </section>
      ) : null}

      {rest.length ? (
        <section aria-label="In words">
          {rest.length > 1 ? <ul className="plain-list bulleted">{rest.map((line, index) => <li key={index}><DocText text={line} /></li>)}</ul> : <p><DocText text={rest[0]} /></p>}
        </section>
      ) : null}
      {components.length ? (
        <section aria-label="The parts in numbers">
          <SectionHead title="The parts in numbers" note="how much code each holds and how it is tied to the rest" />
          <table className="table part-table">
            <thead><tr><th>Component</th><th>Code</th><th>Imports</th><th>Used by</th><th>Guides through it</th></tr></thead>
            <tbody>
              {components.map(component => {
                const guides = walkthroughsThrough(component.path, docs.walkthroughs ?? [], components).length + walkthroughsThrough(component.path, docs.change_guide ?? [], components).length;
                return (
                  <tr key={component.path}>
                    <td><button type="button" className="link-btn mono part-name" onClick={() => navigate('/docs/components', { path: component.path })}>{componentLabel(component)}</button></td>
                    <td>
                      <span className="part-size" title={`${(component.lines ?? 0).toLocaleString()} lines in ${plural(component.files ?? 0, 'file')}`}>
                        <span className="part-bar" aria-hidden><span style={{ width: `${Math.max(2, Math.round(((component.lines ?? 0) / most) * 100))}%` }} /></span>
                        <span className="xs muted">{(component.lines ?? 0).toLocaleString()} lines · {plural(component.files ?? 0, 'file')}</span>
                      </span>
                    </td>
                    <td className="small">{component.imports?.length ? component.imports.map(path => componentLabel({ path })).join(', ') : <span className="faint">nothing</span>}</td>
                    <td className="small">{component.used_by?.length ? component.used_by.map(path => componentLabel({ path })).join(', ') : <span className="faint">nothing</span>}</td>
                    <td className="small">{guides || <span className="faint">none</span>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      ) : null}
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
