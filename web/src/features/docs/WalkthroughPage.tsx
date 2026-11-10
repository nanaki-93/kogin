// Step-by-step guides through the files, as the overview's model traced
// them: walkthroughs (how the main things the project does run) and the
// change guide (where to change the code for the usual changes). Each step
// shows the declaration it stops at, as code, and opens its file.

import { useState } from 'react';
import { ArrowLeft, ArrowRight, ChevronsDownUp, ChevronsUpDown, Route as RouteIcon, Wrench } from 'lucide-react';
import type { ProjectDocs, Walkthrough } from '@/api/types';
import { componentTrail, firstSentence } from '@/lib/docs';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Button, EmptyState } from '@/components/ui';
import { DocText } from './DocText';
import { ComponentTrail, DocTitle, DocsStatusNote } from './GuidePages';
import { StepCode } from './StepCode';

/** What a guide page is: its route, title, entries and the words its empty states use. */
export interface GuideKind {
  to: string;
  title: string;
  /** What the page's guides are, under its title. */
  lead: string;
  icon: typeof RouteIcon;
  aiTitle: string;
  missing: string;
  guides: (docs: ProjectDocs) => Walkthrough[];
}

export const WALKTHROUGHS: GuideKind = {
  to: '/docs/flows', title: 'How it works', icon: RouteIcon,
  lead: 'The main things the project does, each followed through the code from where it starts to where it ends.',
  aiTitle: 'Traced by the analyze model; the files and functions are checked against the code.',
  missing: 'No walkthroughs yet. Refresh docs to write them.',
  guides: docs => docs.walkthroughs ?? [],
};

export const CHANGE_GUIDE: GuideKind = {
  to: '/docs/changes', title: 'Where to change', icon: Wrench,
  lead: 'The changes a developer most often makes here, each as the files and functions to touch, in order.',
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

/** The guides as cards: what each does in a sentence, the components it passes through and how many steps it has. */
export function GuideCards({ docs, kind, columns }: { docs: ProjectDocs; kind: GuideKind; columns?: boolean }) {
  const guides = kind.guides(docs);
  const components = docs.components ?? [];
  if (!guides.length) return <p className="muted">{kind.missing}</p>;
  return (
    <ol className={columns ? 'guide-cards is-columns' : 'guide-cards'}>
      {guides.map((guide, index) => (
        <li key={index}>
          <button type="button" className="guide-card" onClick={() => navigate(kind.to, { n: String(index + 1) })}>
            <span className="guide-card-head">
              <span className="step-number" aria-hidden>{index + 1}</span>
              <span className="strong">{guide.title}</span>
            </span>
            <span className="small muted guide-card-summary">{firstSentence(guide.summary, 200)}</span>
            <span className="guide-card-foot">
              <ComponentTrail trail={componentTrail(guide, components)} />
              <span className="spacer" />
              <span className="xs faint">{plural(guide.steps.length, 'step')}</span>
            </span>
          </button>
        </li>
      ))}
    </ol>
  );
}

function GuideIndex({ docs, kind }: { docs: ProjectDocs; kind: GuideKind }) {
  const guides = kind.guides(docs);
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <DocTitle ai={guides.length > 0} title={kind.aiTitle}>{kind.title}</DocTitle>
        <p className="small muted">{kind.lead}</p>
      </header>
      <GuideCards docs={docs} kind={kind} />
    </article>
  );
}

function GuidePage({ docs, n, kind }: { docs: ProjectDocs; n: number; kind: GuideKind }) {
  const [allCode, setAllCode] = useState(false);
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
  const withCode = guide.steps.filter(step => step.line).length;
  return (
    <article className="doc">
      <DocsStatusNote status={docs.status} />
      <header className="doc-head">
        <div className="xs muted"><button type="button" className="link-btn" onClick={() => navigate(kind.to)}>{kind.title}</button> / {n} of {guides.length}</div>
        <DocTitle ai title={kind.aiTitle}>{guide.title}</DocTitle>
        <p className="lead"><DocText text={guide.summary} /></p>
      </header>
      <div className="guide-bar">
        <ComponentTrail trail={componentTrail(guide, docs.components ?? [])} links />
        <span className="spacer" />
        {withCode > 1 ? <Button size="sm" variant="ghost" icon={allCode ? ChevronsDownUp : ChevronsUpDown} aria-pressed={allCode} onClick={() => setAllCode(!allCode)}>{allCode ? 'Fold the code' : 'Show all the code'}</Button> : null}
      </div>
      <ol className="walkthrough-steps">
        {guide.steps.map((step, index) => (
          <li key={`${n}:${index}`}>
            <span className="step-number" aria-hidden>{index + 1}</span>
            <div className="step-body">
              <div className="step-text"><DocText text={step.text} /></div>
              <StepCode step={step} open={allCode} />
            </div>
          </li>
        ))}
      </ol>
      <div className="row docs-pager">
        {previous ? <Button variant="ghost" icon={ArrowLeft} onClick={() => navigate(kind.to, { n: String(n - 1) })}>{previous.title}</Button> : null}
        <span className="spacer" />
        {next ? <Button variant="ghost" iconRight={ArrowRight} onClick={() => navigate(kind.to, { n: String(n + 1) })}>{next.title}</Button> : null}
      </div>
    </article>
  );
}
