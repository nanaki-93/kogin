// The learning log: what each accepted task taught — the problem it removed,
// the approach and the concepts its change used — plus Study-mode diagnostic
// calibration and 7-domain Engineering Atlas coverage.

import { useMemo, useState } from 'react';
import { Compass, FileCode2, GraduationCap, MessageCircleQuestion, Wrench } from 'lucide-react';
import type { Lesson } from '@/api/types';
import { useLessons } from '@/api/hooks';
import { computeAtlasMastery } from '@/lib/concepts';
import { navigate } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { Badge, Button, EmptyState, Progress, Skeleton, cx } from '@/components/ui';
import { askConceptInProject, ConceptGuideCard } from './ConceptChip';
import { DocText } from './DocText';

/** How you marked your own plan against the agent's. */
const OWN_MATCH: Record<string, string> = { same: 'same cause', partly: 'partly right', missed: 'missed it' };

export function LearningLog() {
  const { data, isLoading } = useLessons();
  const [concept, setConcept] = useState('');
  const lessons = data?.lessons ?? [];
  const concepts = data?.concepts ?? [];
  const mastery = useMemo(() => computeAtlasMastery(lessons), [lessons]);

  if (isLoading) return <Skeleton lines={8} />;
  if (!lessons.length) {
    return (
      <EmptyState
        icon={GraduationCap}
        title="Nothing learned yet"
        action={
          <Button variant="secondary" icon={Compass} onClick={() => navigate('/docs/atlas')}>
            Explore Engineering Atlas
          </Button>
        }
      >
        Each task you accept leaves an entry here: the problem it removed, the approach, and the concepts its change used. Answers from Ask the code can be saved here too.
      </EmptyState>
    );
  }
  const shown = concept ? lessons.filter(lesson => lesson.concepts?.includes(concept)) : lessons;
  return (
    <article className="doc" aria-label="Learning log">
      <div className="row">
        <h1 className="grow">Learning log</h1>
        <Button size="sm" variant="secondary" icon={Compass} onClick={() => navigate('/docs/atlas')}>
          Engineering Atlas
        </Button>
      </div>
      <p className="small muted">What this project has taught you so far: one entry per accepted task, plus the answers you saved from Ask the code. Pick a concept to see every time you met it.</p>

      <section className="mastery-panel" aria-label="Engineering mastery">
        <div className="row-wrap xs">
          <span className="strong">Senior systems breadth</span>
          <span className="muted">
            {mastery.metCurated} of {mastery.totalCurated} Atlas models encountered across {plural(lessons.length, 'entry', 'entries')}
          </span>
          <span className="spacer" />
          {mastery.calibration.written > 0 ? (
            <span className="muted" title="Your Study-mode hypotheses written before reading the agent's plan">
              Diagnostic calibration: <span className="strong">{mastery.calibration.same}</span> same · <span className="strong">{mastery.calibration.partly}</span> partly · <span className="strong">{mastery.calibration.missed}</span> missed
            </span>
          ) : null}
        </div>
        <Progress value={(mastery.metCurated / Math.max(1, mastery.totalCurated)) * 100} tone="accent" label="Atlas models encountered" />
        <div className="mastery-domains">
          {mastery.domains.map(({ domain, metCount, totalCount }) => (
            <button
              key={domain.id}
              type="button"
              className={cx('mastery-domain-pill', metCount > 0 && 'has-met')}
              onClick={() => navigate('/docs/atlas', { domain: domain.id })}
              title={`Open ${domain.id} in Engineering Atlas`}
            >
              <span className="truncate">{domain.short}</span>
              <span className="mono faint">{metCount}/{totalCount}</span>
            </button>
          ))}
        </div>
      </section>

      {concepts.length ? (
        <div className="chips" role="group" aria-label="Concepts">
          {concepts.map(entry => (
            <button key={entry.name} type="button" className={cx('chip', 'chip-concept', concept === entry.name && 'is-on')} aria-pressed={concept === entry.name}
              title={`Met ${plural(entry.count, 'time')} in this log`} onClick={() => setConcept(concept === entry.name ? '' : entry.name)}>
              {entry.name} <span className="faint">{entry.count}</span>
            </button>
          ))}
        </div>
      ) : null}
      {concept ? (
        <>
          <div className="row">
            <span className="xs muted">{plural(shown.length, 'entry', 'entries')} where you met “{concept}”.</span>
            <span className="spacer" />
            <Button size="sm" variant="secondary" icon={MessageCircleQuestion} onClick={() => askConceptInProject(concept)} title="Ask the code agent to explain it with this project's own files">
              Explain “{concept}” in this project
            </Button>
          </div>
          <ConceptGuideCard concept={concept} />
        </>
      ) : null}
      <ol className="lessons" aria-label="Lessons, newest first">
        {shown.map(lesson => (
          <LessonCard key={lesson.id} lesson={lesson} onConcept={name => setConcept(name === concept ? '' : name)} />
        ))}
      </ol>
    </article>
  );
}

function LessonCard({ lesson, onConcept }: { lesson: Lesson; onConcept: (name: string) => void }) {
  const ask = lesson.kind === 'ask';
  return (
    <li className="doc-card lesson">
      <div className="row">
        <Badge tone={ask ? 'info' : 'accent'} icon={ask ? MessageCircleQuestion : Wrench}>{ask ? 'Asked' : 'Shipped'}</Badge>
        <span className="strong small truncate" title={lesson.title}>{lesson.title}</span>
        <span className="spacer" />
        <span className="xs faint nowrap">{timeAgo(lesson.at)}</span>
      </div>
      {lesson.problem ? <p className="small"><span className="muted">Problem:</span> <DocText text={lesson.problem} shorten /></p> : null}
      {lesson.insight ? <p className="small"><span className="muted">{ask ? 'Answer:' : 'Approach:'}</span> <DocText text={lesson.insight} shorten /></p> : null}
      {lesson.own_plan ? (
        <div className="small lesson-own">
          <span className="muted">You wrote first{lesson.own_match ? ` (${OWN_MATCH[lesson.own_match] ?? lesson.own_match})` : ''}:</span>
          {lesson.own_plan.split('\n').map((line, index) => (
            <div key={index}>{line}</div>
          ))}
        </div>
      ) : null}
      {(lesson.concepts?.length || lesson.paths?.length) ? (
        <div className="chips">
          {(lesson.concepts ?? []).map(name => (
            <button key={name} type="button" className="chip chip-concept" onClick={() => onConcept(name)} title="See every time you met this concept">
              {name}
            </button>
          ))}
          {(lesson.paths ?? []).map(path => (
            <button key={path} type="button" className="chip chip-file mono" onClick={() => navigate('/code', { file: path })} title={`Open ${path}`}>
              <FileCode2 size={11} aria-hidden /> {path.slice(path.lastIndexOf('/') + 1)}
            </button>
          ))}
        </div>
      ) : null}
    </li>
  );
}
