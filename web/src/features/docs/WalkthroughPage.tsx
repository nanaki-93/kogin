// Walkthroughs: the main things the project does, step by step through its
// files, as the overview's model traced them. Each step opens its file.

import { ArrowLeft, ArrowRight, Route as RouteIcon } from 'lucide-react';
import type { ProjectDocs } from '@/api/types';
import { componentLabel, componentOf } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Button, EmptyState } from '@/components/ui';
import { DocsStatusNote, FileLink, ModelWritten } from './GuidePages';

export function WalkthroughsIndex({ docs }: { docs: ProjectDocs }) {
  const walkthroughs = docs.walkthroughs ?? [];
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <div className="row"><h1 className="grow">How it works</h1>{walkthroughs.length ? <ModelWritten /> : null}</div>
      <p className="small muted">The main things the project does, traced step by step through its files by the analyze model. Kasuri kept only the steps whose files exist, and links a function only where the file declares it.</p>
      {walkthroughs.length ? (
        <ol className="walkthrough-list">
          {walkthroughs.map((walkthrough, index) => (
            <li key={index}>
              <button type="button" className="component-row" onClick={() => navigate('/docs/flows', { n: String(index + 1) })}>
                <span className="strong">{walkthrough.title}</span>
                <span className="small muted">{walkthrough.summary} · {plural(walkthrough.steps.length, 'step')}</span>
              </button>
            </li>
          ))}
        </ol>
      ) : <p className="muted">No walkthroughs yet. Refresh docs (or run an analysis) to have the analyze model write them.</p>}
    </article>
  );
}

export function WalkthroughPage({ docs, n }: { docs: ProjectDocs; n: number }) {
  const walkthroughs = docs.walkthroughs ?? [];
  const walkthrough = walkthroughs[n - 1];
  const components = docs.components ?? [];
  if (!walkthrough) {
    return (
      <EmptyState icon={RouteIcon} title="No such walkthrough" action={<Button onClick={() => navigate('/docs/flows')}>All walkthroughs</Button>}>
        The overview was written again since this link was made.
      </EmptyState>
    );
  }
  const previous = walkthroughs[n - 2];
  const next = walkthroughs[n];
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <div className="xs muted"><button type="button" className="link-btn" onClick={() => navigate('/docs/flows')}>How it works</button> /</div>
        <h1>{walkthrough.title}</h1>
      </header>
      <p>{walkthrough.summary} <ModelWritten title="Traced by the analyze model; the files and functions are checked against the code." /></p>
      <ol className="walkthrough-steps">
        {walkthrough.steps.map((step, index) => {
          const component = componentOf(step.path, components);
          return (
            <li key={index}>
              <span className="step-number" aria-hidden>{index + 1}</span>
              <div className="step-body">
                <div>{step.text}</div>
                <div className="row-wrap">
                  <FileLink path={step.path} line={step.line} />
                  {step.symbol ? <code className="xs">{step.symbol}</code> : null}
                  {component ? <button type="button" className="chip mono" onClick={() => navigate('/docs/components', { path: component })}>{componentLabel({ path: component })}</button> : null}
                </div>
              </div>
            </li>
          );
        })}
      </ol>
      <div className="row docs-pager">
        {previous ? <Button variant="ghost" icon={ArrowLeft} onClick={() => navigate('/docs/flows', { n: String(n - 1) })}>{previous.title}</Button> : null}
        <span className="spacer" />
        {next ? <Button variant="ghost" iconRight={ArrowRight} onClick={() => navigate('/docs/flows', { n: String(n + 1) })}>{next.title}</Button> : null}
      </div>
    </article>
  );
}
