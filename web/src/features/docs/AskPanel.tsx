// Ask the codebase: the Review role's agent reads the code with read-only
// tools and answers with lines the daemon checked. Answers are not kept by
// the daemon; the page keeps this session's, newest first.

import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { FileCode2, MessageCircleQuestion } from 'lucide-react';
import type { AskAnswer, Project } from '@/api/types';
import { useActions } from '@/app/actions';
import { navigate, useRoute } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { Button, Spinner, Textarea } from '@/components/ui';

// This window's question in flight and answers, so opening a cited line and
// coming back keeps them.
interface AskState {
  asking: string | null;
  answers: AskAnswer[];
  /** A question handed over from elsewhere (the docs' ask box); seq tells two of the same apart. */
  handed: { text: string; seq: number };
}

let state: AskState = { asking: null, answers: [], handed: { text: '', seq: 0 } };
const listeners = new Set<() => void>();

function setAskState(change: Partial<AskState>) {
  state = { ...state, ...change };
  for (const listener of listeners) listener();
}

/** Puts a question in the Ask the code box, even when it is already the route's. */
export function handQuestion(text: string) {
  setAskState({ handed: { text, seq: state.handed.seq + 1 } });
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => void listeners.delete(listener);
}

export function AskPanel({ project, worktreePath }: { project: Project | undefined; worktreePath: string }) {
  const route = useRoute();
  const actions = useActions();
  const { asking, answers, handed } = useSyncExternalStore(subscribe, () => state);
  const suggested = route.params.get('q') ?? '';
  const [question, setQuestion] = useState(suggested);
  const input = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (suggested) setQuestion(suggested);
    input.current?.focus();
  }, [suggested]);
  useEffect(() => {
    if (handed.seq) setQuestion(handed.text);
    input.current?.focus();
  }, [handed]);

  async function ask() {
    const text = question.trim();
    if (!project || !text || state.asking) return;
    setAskState({ asking: text });
    const answer = await actions.askCodebase(project, worktreePath, text);
    setAskState({ asking: null, answers: answer ? [answer, ...state.answers].slice(0, 10) : state.answers });
    if (answer) setQuestion('');
  }

  return (
    <article className="doc">
      <h1>Ask the code</h1>
      <p className="small muted">
        The Review role's agent reads the project's files with Kasuri's read-only tools, starting from these docs, and answers with the lines it used. Kasuri checks that each cited line range holds its quote. Nothing changes.
      </p>
      <form className="ask-form" onSubmit={e => { e.preventDefault(); void ask(); }}>
        <label className="sr-only" htmlFor="ask-question">Question about the code</label>
        <Textarea id="ask-question" ref={input} rows={3} maxLength={2000} value={question} disabled={Boolean(asking)}
          placeholder="How does a task get from the composer to its worktree?" onChange={e => setQuestion(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); void ask(); } }} />
        <div className="row">
          <span className="xs faint grow">Ctrl+Enter to ask · it can take a few minutes</span>
          <Button type="submit" variant="primary" icon={MessageCircleQuestion} disabled={!project || !question.trim()} loading={Boolean(asking)}>Ask</Button>
        </div>
      </form>
      {asking ? (
        <div className="doc-card ask-pending" role="status">
          <div className="row"><Spinner size={14} label="Asking" /> <span className="strong">{asking}</span></div>
          <div className="small muted">The agent is reading the code…</div>
        </div>
      ) : null}
      {answers.map(answer => <Answer key={answer.at + answer.question} answer={answer} />)}
    </article>
  );
}

function Answer({ answer }: { answer: AskAnswer }) {
  return (
    <section className="doc-card ask-answer" aria-label={`Answer to: ${answer.question}`}>
      <h2>{answer.question}</h2>
      <div className="ask-text">{answer.answer}</div>
      <div className="xs muted">Cited lines, each checked against the file:</div>
      <ul className="ask-citations">
        {answer.citations.map(citation => (
          <li key={`${citation.path}:${citation.start_line}-${citation.end_line}`}>
            <button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: citation.path, line: String(citation.start_line) })}
              title={`Open ${citation.path} at line ${citation.start_line}`}>
              <FileCode2 size={12} aria-hidden /> {citation.path}:{citation.start_line}{citation.end_line > citation.start_line ? `–${citation.end_line}` : ''}
            </button>
            <code className="ask-quote">{citation.quote}</code>
          </li>
        ))}
      </ul>
      <div className="xs faint">
        {[answer.model, plural(answer.turns, 'turn'), plural(answer.tool_calls, 'tool call'), timeAgo(answer.at)].filter(Boolean).join(' · ')} · Model-written; the citations are what was checked.
      </div>
    </section>
  );
}
