// The decision log: the design decisions the project records as numbered
// Markdown files in its repository, newest first. Kogin only reads them; a
// decision is written or changed in an ordinary commit, so it is versioned
// and reviewed with the code it is about.

import { useState } from 'react';
import { ChevronRight, Scale } from 'lucide-react';
import { useDecisions } from '@/api/hooks';
import type { Decision } from '@/api/types';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge, EmptyState, Skeleton, cx } from '@/components/ui';
import { DocText } from './DocText';

/** What a record holds, as a file to start from. */
export function decisionTemplate(number: number): string {
  return `# ${number}. <the decision in a few words>

Status: proposed
Date: ${new Date().toISOString().slice(0, 10)}

## Context

What forces the decision: the problem, and what must stay true.

## Options

1. <option> — what it gives, what it costs.
2. <option> — what it gives, what it costs.

## Decision

Which option, and why this one over the others.

## Consequences

What gets easier, what gets harder, what is now ruled out.

## Revisit when

The fact that would make this the wrong choice.
`;
}

const fileNumber = (number: number) => String(number).padStart(4, '0');

export function DecisionLog() {
  const { data, isLoading } = useDecisions();
  const [open, setOpen] = useState<number | null>(null);
  if (isLoading) return <Skeleton lines={8} />;
  const decisions = data?.decisions ?? [];
  const dir = data?.dir || 'docs/decisions';
  const next = Math.max(0, ...decisions.map(decision => decision.number)) + 1;
  const proposed = decisions.filter(decision => decision.status === 'proposed').length;
  if (!decisions.length) {
    return (
      <div className="stack-lg">
        <EmptyState icon={Scale} title="No decisions recorded yet">
          A decision record says what was decided between which options, and why. Kogin reads them from {dir}/ as numbered Markdown files and tells each investigation they exist, so a plan does not quietly go against one. Write the first in an ordinary commit.
        </EmptyState>
        <NewRecord dir={dir} next={next} />
      </div>
    );
  }
  return (
    <article className="doc" aria-label="Decisions">
      <h1>Decisions</h1>
      <p className="small muted">
        The design decisions recorded in {dir}/: what was chosen between which options, and why. {proposed ? `${plural(proposed, 'decision')} still ${proposed === 1 ? 'waits' : 'wait'} for a choice. ` : ''}Each investigation is told they exist.
      </p>
      <ol className="decision-list" aria-label="Decision records">
        {decisions.map(decision => (
          <DecisionRow key={decision.path} decision={decision} open={open === decision.number} onToggle={() => setOpen(open === decision.number ? null : decision.number)} />
        ))}
      </ol>
      <NewRecord dir={dir} next={next} />
    </article>
  );
}

function statusTone(status: string | undefined): 'success' | 'warning' | 'neutral' {
  if (status === 'accepted') return 'success';
  if (status === 'proposed') return 'warning';
  return 'neutral';
}

function DecisionRow({ decision, open, onToggle }: { decision: Decision; open: boolean; onToggle: () => void }) {
  return (
    <li className={cx('decision', open && 'is-open')}>
      <button type="button" className="decision-head" onClick={onToggle} aria-expanded={open}>
        <ChevronRight size={14} className="decision-caret" aria-hidden />
        <span className="mono xs faint">{fileNumber(decision.number)}</span>
        <span className="strong grow">{decision.title}</span>
        {decision.date ? <span className="xs faint nowrap">{decision.date}</span> : null}
        <Badge tone={statusTone(decision.status)}>{decision.status || 'no status'}</Badge>
      </button>
      {open ? (
        <div className="decision-body">
          {(decision.sections ?? []).map(section => (
            <section key={section.heading}>
              <div className="label-row">{section.heading}</div>
              <p className="prose small decision-text">
                <DocText text={section.text} />
              </p>
            </section>
          ))}
          <button type="button" className="link-btn xs mono" onClick={() => navigate('/code', { file: decision.path })} title="Open the record in Code">
            {decision.path}
          </button>
        </div>
      ) : null}
    </li>
  );
}

/** How to add a record: the file to create and what goes in it. */
function NewRecord({ dir, next }: { dir: string; next: number }) {
  return (
    <details className="plan-why decision-new">
      <summary className="small">
        Record a decision
        <span className="xs muted"> · a file you commit, {dir}/{fileNumber(next)}-short-title.md</span>
      </summary>
      <p className="small muted">Write it before the code when you can: “proposed” while you weigh the options, “accepted” once you choose. A later decision that replaces it says “superseded by {fileNumber(next + 1)}” here instead of editing history.</p>
      <pre className="decision-template">{decisionTemplate(next)}</pre>
    </details>
  );
}
