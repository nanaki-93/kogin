// The learning log: what each accepted task taught — the problem it removed,
// the approach and the concepts its change used — and the Ask the code
// answers saved to it. It accumulates per project, so meeting a concept
// again links back to where it was met before.

import { useState } from 'react';
import { FileCode2, GraduationCap, MessageCircleQuestion, Wrench } from 'lucide-react';
import type { Lesson } from '@/api/types';
import { useLessons } from '@/api/hooks';
import { navigate } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { Badge, Button, EmptyState, Skeleton, cx } from '@/components/ui';
import { handQuestion } from './AskPanel';

/** Hands Ask the code a question about the concept, answered from this project's files. */
function explainConcept(concept: string) {
  const question = `Explain how this project uses ${concept}: where it appears, why it is used there, and what a newcomer should know about it.`;
  handQuestion(question);
  navigate('/docs/ask', { q: question });
}

/** How you marked your own plan against the agent's. */
const OWN_MATCH: Record<string, string> = { same: 'same cause', partly: 'partly right', missed: 'missed it' };

export function LearningLog() {
  const { data, isLoading } = useLessons();
  const [concept, setConcept] = useState('');
  if (isLoading) return <Skeleton lines={8} />;
  const lessons = data?.lessons ?? [];
  const concepts = data?.concepts ?? [];
  if (!lessons.length) {
    return (
      <EmptyState icon={GraduationCap} title="Nothing learned yet">
        Each task you accept leaves an entry here: the problem it removed, the approach, and the concepts its change used. Answers from Ask the code can be saved here too.
      </EmptyState>
    );
  }
  const shown = concept ? lessons.filter(lesson => lesson.concepts?.includes(concept)) : lessons;
  return (
    <article className="doc" aria-label="Learning log">
      <h1>Learning log</h1>
      <p className="small muted">What this project has taught you so far: one entry per accepted task, plus the answers you saved from Ask the code. Pick a concept to see every time you met it.</p>
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
        <div className="row">
          <span className="xs muted">{plural(shown.length, 'entry', 'entries')} where you met “{concept}”.</span>
          <span className="spacer" />
          <Button size="sm" variant="secondary" icon={MessageCircleQuestion} onClick={() => explainConcept(concept)} title="Ask the code agent to explain it with this project's own files">
            Explain “{concept}” in this project
          </Button>
        </div>
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
      {lesson.problem ? <p className="small"><span className="muted">Problem:</span> {lesson.problem}</p> : null}
      {lesson.insight ? <p className="small"><span className="muted">{ask ? 'Answer:' : 'Approach:'}</span> {lesson.insight}</p> : null}
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
