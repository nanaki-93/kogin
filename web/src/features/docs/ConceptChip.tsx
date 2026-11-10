// Interactive Go & software-engineering micro-card for concept chips surfaced
// on hunks and in the Learning log. Opening a card never calls a model;
// exploring how this project uses the concept hands a pre-filled question to
// Ask the code.

import { useMemo, useState } from 'react';
import * as Popover from '@radix-ui/react-popover';
import { AlertTriangle, BookOpen, GraduationCap, MessageCircleQuestion, X } from 'lucide-react';
import { useLessons } from '@/api/hooks';
import { conceptQuestion, lookupConcept, type ConceptCategory } from '@/lib/concepts';
import { highlightLines } from '@/lib/highlight';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge, Button, cx } from '@/components/ui';
import { handQuestion } from './AskPanel';

const CATEGORY_TONE: Record<ConceptCategory, 'accent' | 'info' | 'warning' | 'success' | 'neutral'> = {
  'Go idiom': 'accent',
  Concurrency: 'info',
  Safety: 'warning',
  Testing: 'success',
  Design: 'info',
  'Project pattern': 'neutral',
};

export function askConceptInProject(concept: string): void {
  const question = conceptQuestion(concept);
  handQuestion(question);
  navigate('/docs/ask', { q: question });
}

function ConceptBody({ concept, showProjectHistory = true }: { concept: string; showProjectHistory?: boolean }) {
  const guide = useMemo(() => lookupConcept(concept), [concept]);
  const highlighted = useMemo(() => (guide.code ? highlightLines(guide.code, 'go') : []), [guide.code]);
  const lessons = useLessons().data?.lessons;
  const matching = useMemo(() => {
    if (!showProjectHistory || !lessons?.length) return [];
    const needle = concept.trim().toLowerCase();
    return lessons.filter(entry => (entry.concepts ?? []).some(tag => tag.trim().toLowerCase() === needle));
  }, [concept, lessons, showProjectHistory]);

  return (
    <div className="concept-body">
      <p className="concept-what">{guide.what}</p>
      <p className="concept-ingo small">
        <span className="strong">In Go: </span>
        {guide.inGo}
      </p>
      {highlighted.length ? (
        <pre className="concept-code" aria-label={`Go example for ${guide.title}`}>
          {highlighted.map((html, idx) => (
            <code key={idx} dangerouslySetInnerHTML={{ __html: html || ' ' }} />
          ))}
        </pre>
      ) : null}
      {guide.watchOut ? (
        <div className="concept-watch xs">
          <AlertTriangle size={12} className="tone-warning" aria-hidden />
          <span>{guide.watchOut}</span>
        </div>
      ) : null}
      {showProjectHistory ? (
        <div className="concept-seen xs muted">
          {matching.length ? (
            <>
              Met {plural(matching.length, 'time')} in this project:{' '}
              <span className="concept-seen-titles">
                {matching.slice(0, 2).map(item => item.title).join(' · ')}
              </span>
            </>
          ) : (
            'First time here — accepting this task records it in Docs → Learning log.'
          )}
        </div>
      ) : null}
    </div>
  );
}

/** Inline card rendered in Docs → Learning log when filtering by a curated concept. */
export function ConceptGuideCard({ concept }: { concept: string }) {
  const guide = useMemo(() => lookupConcept(concept), [concept]);
  if (!guide.curated) return null;
  return (
    <section className="concept-inline-card" aria-label={`Guide for ${guide.title}`}>
      <div className="row">
        <Badge tone={CATEGORY_TONE[guide.category]} icon={GraduationCap}>{guide.category}</Badge>
        <span className="strong small">{guide.title}</span>
      </div>
      <ConceptBody concept={concept} showProjectHistory={false} />
    </section>
  );
}

/** Clickable concept pill that opens an interactive micro-card popover in place. */
export function ConceptChip({ concept }: { concept: string }) {
  const [open, setOpen] = useState(false);
  const guide = useMemo(() => lookupConcept(concept), [concept]);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button
          type="button"
          className={cx('chip', 'chip-concept', 'chip-concept-btn', open && 'is-on')}
          onClick={event => event.stopPropagation()}
          title={`Learn about ${concept}`}
        >
          <GraduationCap size={11} aria-hidden />
          <span>{concept}</span>
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className="concept-popover"
          side="top"
          align="start"
          sideOffset={6}
          collisionPadding={12}
          onClick={event => event.stopPropagation()}
          aria-label={`Concept: ${guide.title}`}
        >
          <div className="concept-head">
            <Badge tone={CATEGORY_TONE[guide.category]}>{guide.category}</Badge>
            <span className="concept-title truncate" title={guide.title}>{guide.title}</span>
            <span className="spacer" />
            <Popover.Close asChild>
              <button type="button" className="icon-btn icon-btn-sm" aria-label="Close concept card">
                <X size={13} aria-hidden />
              </button>
            </Popover.Close>
          </div>
          <ConceptBody concept={concept} />
          <div className="concept-foot">
            <Button size="sm" variant="ghost" icon={BookOpen} onClick={() => { setOpen(false); navigate('/docs/log'); }}>
              Learning log
            </Button>
            <Button size="sm" variant="secondary" icon={MessageCircleQuestion} onClick={() => { setOpen(false); askConceptInProject(concept); }}>
              Ask in project
            </Button>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
