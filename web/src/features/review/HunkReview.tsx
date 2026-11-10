// The review as plan vs result: every hunk of the change on one page, each
// under the note that says what it fixes, set against the plan you approved.
// What the plan did not call for, and what nobody explained, comes first;
// the plan's steps follow in order. A marks the current hunk right and moves
// on (a file whose hunks are all right is marked viewed); M comments on its
// first changed line; ← → (or j k) move between hunks.

import { useEffect, useMemo, useRef, useState } from 'react';
import { AlertTriangle, Check, CircleCheck, MessageSquare, Minus } from 'lucide-react';
import type { DiffLine, FileChange, LoadedSession, WalkthroughNote } from '@/api/types';
import { useActions } from '@/app/actions';
import { usePageKeys } from '@/app/shortcuts';
import type { DiffLayout } from '@/app/store';
import { HUNK_KINDS, hunkAnchor, hunkHasLine, hunkKey, loadVerdicts, mergedReviewNotes, planResult, reviewHunks, saveVerdicts, type Hunk, type PlanResult, type ReviewBlock } from '@/lib/hunks';
import { behaviorChanges, checkSummary, isFileReviewed } from '@/lib/readiness';
import { navigate, useRoute } from '@/lib/router';
import { reviewVerdict } from '@/lib/workflow';
import { basename, languageOf, plural } from '@/lib/util';
import { Badge, Button, IconButton, Kbd, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';
import { DocText } from '@/features/docs/DocText';
import { ConceptChip } from '@/features/docs/ConceptChip';
import { useOpenTaskFile } from '@/features/tasks/TaskCodePreview';
import { ReviewItems } from '@/features/tasks/ReviewItems';
import { ChangeNotes, goalLabel } from './ChangeNotes';
import { DiffView } from './DiffView';
import { FileStatus } from './FileStatus';
import { useFileComments } from './LineComments';

export function HunkReview({ session, edits, layout, wrap, canComment, focus, canMark }: {
  session: LoadedSession;
  edits: FileChange[];
  layout: DiffLayout;
  wrap: boolean;
  /** Whether line comments can be added to this version. */
  canComment: (path: string) => boolean;
  focus?: number;
  /** Whether the file can be marked viewed now. */
  canMark: (path: string) => boolean;
}) {
  const route = useRoute();
  const actions = useActions();
  const linesOf = useMemo(() => new Map<string, DiffLine[]>(edits.map(edit => [edit.path, edit.diff?.lines ?? []])), [edits]);
  const result = useMemo(() => planResult(session, linesOf, reviewHunks(edits)), [session, linesOf, edits]);
  const all = result.blocks;
  const [verdicts, setVerdicts] = useState(() => loadVerdicts(session.id, session.hash));
  useEffect(() => setVerdicts(loadVerdicts(session.id, session.hash)), [session.id, session.hash]);
  const [commenting, setCommenting] = useState('');

  const index = currentBlock(all, linesOf, route.params.get('file') ?? '', Number(route.params.get('hunk') ?? -1), focus);
  const block = all[index];
  const right = (hunk: Hunk) => verdicts.has(hunkKey(hunk)) || isFileReviewed(session, hunk.path);
  const go = (to: number) => {
    const target = all[(to + all.length) % all.length];
    if (target) navigate(route.path, { ...Object.fromEntries(route.params.entries()), file: target.hunk.path, hunk: String(target.hunk.n), line: undefined }, { replace: true });
  };

  async function looksRight(at: number) {
    const target = all[at];
    if (!target) return;
    const next = new Set(verdicts).add(hunkKey(target.hunk));
    setVerdicts(next);
    saveVerdicts(session.id, session.hash, next);
    const settled = (hunk: Hunk) => next.has(hunkKey(hunk)) || isFileReviewed(session, hunk.path);
    if (all.filter(entry => entry.hunk.path === target.hunk.path).every(entry => settled(entry.hunk)) && canMark(target.hunk.path)) await actions.markViewed(session, target.hunk.path);
    const after = all.findIndex((entry, i) => i > at && !settled(entry.hunk));
    const before = all.findIndex(entry => !settled(entry.hunk));
    const to = after >= 0 ? after : before;
    if (to >= 0 && to !== index) go(to);
  }

  usePageKeys(key => {
    if (key === 'ArrowRight' || key === 'j') go(index + 1);
    else if (key === 'ArrowLeft' || key === 'k') go(index - 1);
    else if (key === 'a') void looksRight(index);
    else if (key === 'm' && block && canComment(block.hunk.path)) setCommenting(hunkKey(block.hunk));
    else return false;
    return true;
  });

  // The current hunk comes into view when it changes, by key or from the summary.
  // The page opens on the summary; a hunk named by the link, or a focused line, is shown at once.
  const scroller = useRef<HTMLDivElement>(null);
  const currentKey = block ? hunkKey(block.hunk) : '';
  const opened = useRef(!route.params.get('hunk') && !focus);
  useEffect(() => {
    if (opened.current) opened.current = false;
    else scroller.current?.querySelector('.rv-block.is-current')?.scrollIntoView({ block: 'nearest' });
  }, [currentKey]);

  // A moved or emptied file changes no line: it is listed under the hunks.
  const untouched = edits.filter(edit => !all.some(entry => entry.hunk.path === edit.path));
  if (!block) return <div className="diff-empty muted small">No textual changes to review.</div>;
  return (
    <div className="rv" ref={scroller}>
      <ResultSummary session={session} result={result} right={right} onOpen={target => go(all.indexOf(target))} />
      <ol className="rv-blocks" aria-label="Changes, in reading order">
        {all.map((entry, i) => (
          <ReviewHunk
            key={hunkKey(entry.hunk) + session.hash}
            session={session}
            change={edits.find(edit => edit.path === entry.hunk.path)}
            block={entry}
            lines={linesOf.get(entry.hunk.path) ?? []}
            mapped={result.mapped}
            current={i === index}
            right={right(entry.hunk)}
            layout={layout}
            wrap={wrap}
            focus={i === index ? focus : undefined}
            canComment={canComment(entry.hunk.path)}
            commenting={commenting === hunkKey(entry.hunk)}
            onCommented={() => setCommenting('')}
            onSelect={() => (i === index ? undefined : go(i))}
            onRight={() => void looksRight(i)}
          />
        ))}
      </ol>
      {untouched.length ? (
        <ul className="rv-rows rv-untouched" aria-label="Files without textual changes">
          {untouched.map(edit => (
            <li key={edit.path} className="rv-row">
              <FileIcon path={edit.path} size={13} />
              <span className="grow mono small truncate">{edit.path}</span>
              <FileStatus change={edit} />
              <span className="xs faint">no textual changes</span>
            </li>
          ))}
        </ul>
      ) : null}
      <p className="xs faint rv-keys">
        <Kbd>←</Kbd> <Kbd>→</Kbd> hunks · <Kbd>A</Kbd> looks right · <Kbd>M</Kbd> comment · <Kbd>F</Kbd> whole file
      </p>
    </div>
  );
}

/** The change against the plan in one glance: what went as approved, what did not, and what the checks and reviewers said. */
function ResultSummary({ session, result, right, onOpen }: { session: LoadedSession; result: PlanResult; right: (hunk: Hunk) => boolean; onOpen: (block: ReviewBlock) => void }) {
  const checks = checkSummary(session.checks, session.checks_outdated);
  const verdict = reviewVerdict(session.workflow, session.hash);
  const behavior = behaviorChanges(session);
  const suggestions = mergedReviewNotes(session.workflow, session.hash);
  const exceptions = result.blocks.filter(block => block.exception);
  return (
    <section className="rv-summary" aria-label="Plan vs result">
      <div className="row-wrap">
        <span className="strong">{result.mapped ? 'Plan vs result' : 'What changed'}</span>
        <span className="spacer" />
        {checks.total ? (
          <button type="button" className={cx('badge', `badge-${checks.tone}`)} onClick={() => navigate(`/tasks/${session.id}/checks`)} title="Open the checks">
            {checks.tone === 'success' ? `${plural(checks.total, 'check')} passed` : `Checks: ${checks.label.toLowerCase()}`}
          </button>
        ) : null}
        {verdict === 'approve' || verdict === 'changes_requested' ? <Badge tone={verdict === 'approve' ? 'success' : 'danger'}>{verdict === 'approve' ? 'Reviewers approved' : 'Reviewers asked for changes'}</Badge> : null}
        {behavior.length ? (
          <button type="button" className="badge badge-warning" onClick={() => navigate(`/tasks/${session.id}`)} title="Open the behavior changes">
            {plural(behavior.length, 'behavior change')}
          </button>
        ) : null}
      </div>
      {result.mapped ? (
        <ul className="rv-rows">
          {exceptions.map(block => (
            <li key={hunkKey(block.hunk)}>
              <button type="button" className="rv-row is-exception" onClick={() => onOpen(block)}>
                <AlertTriangle size={14} className="tone-warning" aria-hidden />
                <span className="grow">
                  <span className="strong">{block.exception === 'outside' ? 'Not in your plan' : 'Not explained'}</span>
                  {block.notes[0] ? <> · <DocText text={block.notes[0].problem} shorten /></> : null}
                </span>
                <span className="mono xs faint">{blockPlace(block)}</span>
              </button>
            </li>
          ))}
          {result.steps.map(step => (
            <li key={step.n}>
              <button type="button" className="rv-row" onClick={() => (step.blocks[0] ? onOpen(step.blocks[0]) : undefined)} disabled={!step.blocks.length}>
                {step.blocks.length ? step.blocks.every(block => right(block.hunk)) ? <CircleCheck size={14} className="tone-success" aria-hidden /> : <Check size={14} className="tone-success" aria-hidden /> : <Minus size={14} className="faint" aria-hidden />}
                <span className="grow">
                  <span className="muted">{step.n} · </span>
                  <DocText text={step.text} shorten />
                </span>
                <span className="mono xs faint">{step.blocks.length ? step.blocks.map(blockPlace).join(', ') : 'no lines changed'}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <ChangeNotes session={session} path={result.blocks[0]?.hunk.path ?? ''} unplaced={[]} />
      )}
      {suggestions.length ? (
        <details className="rv-suggestions">
          <summary className="small muted">
            {plural(suggestions.length, 'suggestion')} from the reviewers
            <span className="xs faint"> · advice, not checked against the code; none blocks the change</span>
          </summary>
          <ReviewItems items={suggestions.map(entry => ({ ...entry.item, title: entry.reviewers.length > 1 ? `${entry.item.title} (${entry.reviewers.length} reviewers)` : entry.item.title }))} session={session} advisory />
        </details>
      ) : null}
    </section>
  );
}

/** Where a hunk is: its file and the first line it changes. */
function blockPlace(block: ReviewBlock): string {
  const line = block.notes.find(note => note.line)?.line;
  return `${basename(block.hunk.path)}${line ? `:${line}` : ''}`;
}

/** One hunk: what it is to the plan, the note on what it fixes, and its lines. */
function ReviewHunk({ session, change, block, lines, mapped, current, right, layout, wrap, focus, canComment, commenting, onCommented, onSelect, onRight }: {
  session: LoadedSession;
  change?: FileChange;
  block: ReviewBlock;
  lines: DiffLine[];
  mapped: boolean;
  current: boolean;
  right: boolean;
  layout: DiffLayout;
  wrap: boolean;
  focus?: number;
  canComment: boolean;
  commenting: boolean;
  onCommented: () => void;
  onSelect: () => void;
  onRight: () => void;
}) {
  const { hunk, notes } = block;
  const openTaskFile = useOpenTaskFile(session.checkout.worktreePath);
  const comments = useFileComments(session, hunk.path, canComment);
  // M opens the form on the hunk's first changed line.
  useEffect(() => {
    if (!commenting) return;
    const anchor = hunkAnchor(lines, hunk);
    if (anchor) comments.onAdd?.(anchor);
    onCommented();
  }, [commenting]); // eslint-disable-line react-hooks/exhaustive-deps
  const kind = HUNK_KINDS[block.kind ?? ''];
  const tag = block.exception === 'outside' ? { text: 'Not in your plan', tone: 'warning' as const }
    : block.exception === 'unexplained' ? { text: 'Not explained', tone: 'warning' as const }
    : mapped && block.planStep ? { text: `Step ${block.planStep}`, tone: 'accent' as const }
    : mapped && !block.exception ? { text: 'Proof', tone: 'info' as const }
    : kind ? { text: kind.label, tone: kind.tone } : null;
  const firstLine = notes.find(note => note.line)?.line ?? hunkAnchor(lines, hunk)?.line;
  return (
    <li className={cx('rv-block', layout === 'split' && 'is-split-layout', current && 'is-current', block.exception && 'is-exception', right && 'is-right')} aria-current={current ? 'step' : undefined} onClick={onSelect}>
      <div className="rv-main">
        <div className="rv-code-bar">
          <button type="button" className="chip chip-file" title={`Inspect ${hunk.path} in place`} onClick={event => { event.stopPropagation(); openTaskFile(hunk.path, firstLine); }}>
            <FileIcon path={hunk.path} size={12} />
            {blockPlace(block)}
          </button>
          {change ? <FileStatus change={change} /> : null}
          <span className="spacer" />
          {canComment ? <IconButton icon={MessageSquare} label="Comment on this hunk (M)" size="sm" onClick={event => { event.stopPropagation(); const anchor = hunkAnchor(lines, hunk); if (anchor) comments.onAdd?.(anchor); }} /> : null}
        </div>
        <div className="rv-code">
          <DiffView lines={lines.slice(hunk.start, hunk.end)} language={languageOf(hunk.path)} layout={layout} wrap={wrap} anchorPrefix={`h-${hunkKey(hunk)}`} comments={comments} focus={focus} />
        </div>
      </div>
      <aside className="rv-margin" aria-label="Hunk note">
        <div className="rv-margin-top">
          {tag ? <Badge tone={tag.tone}>{tag.text}</Badge> : null}
          <span className="spacer" />
          <Button size="sm" variant={right ? 'ghost' : current ? 'primary' : 'secondary'} icon={right ? CircleCheck : Check} onClick={event => { event.stopPropagation(); onRight(); }} kbd={current && !right ? 'A' : undefined}>
            {right ? 'Right' : 'Looks right'}
          </Button>
        </div>
        {notes.length ? (
          <div className="rv-notes" aria-label="What this hunk fixes">
            {notes.map((note, i) => (
              <BlockNote key={i} session={session} note={note} />
            ))}
          </div>
        ) : (
          <p className="small muted rv-notes">{block.exception === 'unexplained' ? 'The explainer left no note on this hunk: read it yourself.' : 'No note for this hunk.'}</p>
        )}
      </aside>
    </li>
  );
}

/** A note on its hunk: the wrong behavior that goes away, then how, in one muted line. */
function BlockNote({ session, note }: { session: LoadedSession; note: WalkthroughNote }) {
  const concepts = note.concepts ?? [];
  return (
    <div className="rv-note">
      <p>
        <DocText text={note.problem} shorten />
      </p>
      {note.how || note.goal ? (
        <p className="small muted">
          {note.how ? <DocText text={note.how} shorten /> : null}
          {note.how && note.goal ? ' · ' : null}
          {note.goal ? <span title={goalLabel(session, note)}>{goalLabel(session, note).split(' · ')[0]}</span> : null}
        </p>
      ) : null}
      {concepts.length ? (
        <div className="rv-concepts">
          {concepts.map(concept => (
            <ConceptChip key={concept} concept={concept} />
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** The block the route names; else the one showing the focused line, else the first. */
function currentBlock(all: ReviewBlock[], linesOf: Map<string, DiffLine[]>, path: string, n: number, focus?: number): number {
  const named = all.findIndex(block => block.hunk.path === path && block.hunk.n === n);
  if (named >= 0) return named;
  if (focus) {
    const showing = all.findIndex(block => block.hunk.path === path && hunkHasLine(linesOf.get(path) ?? [], block.hunk, focus));
    if (showing >= 0) return showing;
  }
  // A file picked from the list opens at its first hunk in reading order.
  return Math.max(0, path ? all.findIndex(block => block.hunk.path === path) : 0);
}
