// Step-by-step guides through the files, as the overview's model traced
// them: walkthroughs (how the main things the project does run) and the
// change guide (where to change the code for the usual changes). Each step
// opens its file.

import { ArrowLeft, ArrowRight, Route as RouteIcon, Wrench } from 'lucide-react';
import type { ProjectDocs, Walkthrough } from '@/api/types';
import { componentLabel, componentOf, firstSentence } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Button, EmptyState } from '@/components/ui';
import { DocText } from './DocText';
import { DocTitle, DocsStatusNote, FileLink } from './GuidePages';

/** What a guide page is: its route, title, entries and the words its empty states use. */
interface GuideKind {
  to: string;
  title: string;
  icon: typeof RouteIcon;
  aiTitle: string;
  missing: string;
  guides: (docs: ProjectDocs) => Walkthrough[];
}

const WALKTHROUGHS: GuideKind = {
  to: '/docs/flows', title: 'How it works', icon: RouteIcon,
  aiTitle: 'Traced by the analyze model; the files and functions are checked against the code.',
  missing: 'No walkthroughs yet. Refresh docs to write them.',
  guides: docs => docs.walkthroughs ?? [],
};

const CHANGE_GUIDE: GuideKind = {
  to: '/docs/changes', title: 'Where to change', icon: Wrench,
  aiTitle: 'Written by the analyze model; the files and functions are checked against the code.',
  missing: 'Comes with the next overview. Refresh docs to write it.',
  guides: docs => docs.change_guide ?? [],
};

export function WalkthroughsIndex({ docs }: { docs: ProjectDocs }) {
  return <GuideIndex docs={docs} kind={WALKTHROUGHS} />;
}

export function WalkthroughPage({ docs, n }: { docs: ProjectDocs; n: number }) {
  return <GuidePage docs={docs} n={n} kind={WALKTHROUGHS} />;
}

export function ChangeGuideIndex({ docs }: { docs: ProjectDocs }) {
  return <GuideIndex docs={docs} kind={CHANGE_GUIDE} />;
}

export function ChangeGuidePage({ docs, n }: { docs: ProjectDocs; n: number }) {
  return <GuidePage docs={docs} n={n} kind={CHANGE_GUIDE} />;
}

function GuideIndex({ docs, kind }: { docs: ProjectDocs; kind: GuideKind }) {
  const guides = kind.guides(docs);
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <DocTitle ai={guides.length > 0} title={kind.aiTitle}>{kind.title}</DocTitle>
      {guides.length ? (
        <ol className="walkthrough-list">
          {guides.map((guide, index) => (
            <li key={index}>
              <button type="button" className="component-row" onClick={() => navigate(kind.to, { n: String(index + 1) })}>
                <span className="strong">{guide.title}</span>
                <span className="small muted">{firstSentence(guide.summary, 200)} · {plural(guide.steps.length, 'step')}</span>
              </button>
            </li>
          ))}
        </ol>
      ) : <p className="muted">{kind.missing}</p>}
    </article>
  );
}

function GuidePage({ docs, n, kind }: { docs: ProjectDocs; n: number; kind: GuideKind }) {
  const guides = kind.guides(docs);
  const guide = guides[n - 1];
  if (!guide) {
    return (
      <EmptyState icon={kind.icon} title="Not in the docs any more" action={<Button onClick={() => navigate(kind.to)}>{kind.title}</Button>}>
        The overview was written again since this link was made.
      </EmptyState>
    );
  }
  const previous = guides[n - 2];
  const next = guides[n];
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <div className="xs muted"><button type="button" className="link-btn" onClick={() => navigate(kind.to)}>{kind.title}</button> /</div>
        <DocTitle ai title={kind.aiTitle}>{guide.title}</DocTitle>
      </header>
      <p><DocText text={guide.summary} /></p>
      <GuideSteps guide={guide} docs={docs} />
      <div className="row docs-pager">
        {previous ? <Button variant="ghost" icon={ArrowLeft} onClick={() => navigate(kind.to, { n: String(n - 1) })}>{previous.title}</Button> : null}
        <span className="spacer" />
        {next ? <Button variant="ghost" iconRight={ArrowRight} onClick={() => navigate(kind.to, { n: String(n + 1) })}>{next.title}</Button> : null}
      </div>
    </article>
  );
}

function GuideSteps({ guide, docs }: { guide: Walkthrough; docs: ProjectDocs }) {
  const components = docs.components ?? [];
  return (
    <ol className="walkthrough-steps">
      {guide.steps.map((step, index) => {
        const component = componentOf(step.path, components);
        return (
          <li key={index}>
            <span className="step-number" aria-hidden>{index + 1}</span>
            <div className="step-body">
              <div><DocText text={step.text} /></div>
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
  );
}
