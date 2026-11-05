// Focus review: the change one hunk at a time, the note on what it fixes
// beside it. A marks the hunk right and moves on; once every hunk of a file
// is right the file is marked viewed. M comments on the hunk's first changed
// line (M); ← → (or j k) move between hunks across files.

import { useEffect, useMemo, useState } from 'react';
import { Check, ChevronLeft, ChevronRight, MessageSquare } from 'lucide-react';
import type { DiffLine, FileChange, LoadedSession } from '@/api/types';
import { useActions } from '@/app/actions';
import { usePageKeys } from '@/app/shortcuts';
import type { DiffLayout } from '@/app/store';
import { noteStart } from '@/lib/diff';
import { hunkAnchor, hunkHasLine, hunkKey, loadVerdicts, reviewHunks, saveVerdicts, type Hunk } from '@/lib/hunks';
import { isFileReviewed } from '@/lib/readiness';
import { navigate, useRoute } from '@/lib/router';
import { languageOf } from '@/lib/util';
import { Button, IconButton, Kbd, cx } from '@/components/ui';
import { goalLabel, HunkNote, walkthroughFile } from './ChangeNotes';
import { DiffView, type DiffComments } from './DiffView';

export function HunkReview({ session, edits, layout, wrap, comments, focus, canMark }: {
  session: LoadedSession;
  edits: FileChange[];
  layout: DiffLayout;
  wrap: boolean;
  comments?: DiffComments;
  focus?: number;
  /** Whether the file can be marked viewed now. */
  canMark: (path: string) => boolean;
}) {
  const route = useRoute();
  const actions = useActions();
  const all = useMemo(() => reviewHunks(edits), [edits]);
  const linesOf = useMemo(() => new Map<string, DiffLine[]>(edits.map(edit => [edit.path, edit.diff?.lines ?? []])), [edits]);
  const [verdicts, setVerdicts] = useState(() => loadVerdicts(session.id, session.hash));
  useEffect(() => setVerdicts(loadVerdicts(session.id, session.hash)), [session.id, session.hash]);

  const index = currentHunk(all, linesOf, route.params.get('file') ?? '', Number(route.params.get('hunk') ?? -1), focus);
  const hunk = all[index];
  const right = (h: Hunk) => verdicts.has(hunkKey(h)) || isFileReviewed(session, h.path);
  const go = (to: number) => {
    const target = all[(to + all.length) % all.length];
    if (target) navigate(route.path, { ...Object.fromEntries(route.params.entries()), file: target.path, hunk: String(target.n), line: undefined }, { replace: true });
  };

  async function looksRight() {
    if (!hunk) return;
    const next = new Set(verdicts).add(hunkKey(hunk));
    setVerdicts(next);
    saveVerdicts(session.id, session.hash, next);
    const fileDone = all.filter(h => h.path === hunk.path).every(h => next.has(hunkKey(h)) || isFileReviewed(session, h.path));
    if (fileDone && canMark(hunk.path)) await actions.markViewed(session, hunk.path);
    const pending = all.findIndex((h, i) => i > index && !next.has(hunkKey(h)) && !isFileReviewed(session, h.path));
    const before = all.findIndex(h => !next.has(hunkKey(h)) && !isFileReviewed(session, h.path));
    const to = pending >= 0 ? pending : before;
    if (to >= 0 && to !== index) go(to);
  }
  const comment = () => {
    const anchor = hunk ? hunkAnchor(linesOf.get(hunk.path) ?? [], hunk) : null;
    if (anchor) comments?.onAdd?.(anchor);
  };

  usePageKeys(key => {
    if (key === 'ArrowRight' || key === 'j') go(index + 1);
    else if (key === 'ArrowLeft' || key === 'k') go(index - 1);
    else if (key === 'a') void looksRight();
    else if (key === 'm' && comments?.onAdd) comment();
    else return false;
    return true;
  });

  if (!hunk) return <div className="diff-empty muted small">No textual changes to review.</div>;
  const lines = linesOf.get(hunk.path) ?? [];
  const inFile = all.filter(h => h.path === hunk.path);
  const file = walkthroughFile(session, hunk.path);
  const notes = (file?.notes ?? []).filter(note => {
    const at = note.line ? noteStart(lines, note.line) : -1;
    return at >= hunk.start && at < hunk.end;
  });
  const done = all.filter(right).length;

  return (
    <div className="hunk-review">
      <aside className="hunk-pane" aria-label="This hunk">
        <div className="hunk-count">
          <span className="grow">
            <span className="strong">Hunk {index + 1} of {all.length}</span>
            <span className="xs muted">{inFile.length > 1 ? `${hunk.n + 1} of ${inFile.length} in this file · ` : ''}{done} right</span>
          </span>
          <IconButton icon={ChevronLeft} label="Previous hunk (←)" size="sm" onClick={() => go(index - 1)} />
          <IconButton icon={ChevronRight} label="Next hunk (→)" size="sm" onClick={() => go(index + 1)} />
        </div>
        <div className="hunk-dots" aria-hidden>
          {all.map((h, i) => (
            <button key={hunkKey(h)} type="button" tabIndex={-1} className={cx('hunk-dot', right(h) && 'is-right', i === index && 'is-current', h.path !== all[i - 1]?.path && i > 0 && 'is-file')} title={`${h.path} · hunk ${h.n + 1}`} onClick={() => go(i)} />
          ))}
        </div>
        <div className="hunk-notes">
          {notes.length ? (
            notes.map((note, i) => <HunkNote key={i} note={note} goal={goalLabel(session, note)} />)
          ) : (
            <p className="small muted">{file ? 'No note for this hunk.' : session.walkthrough ? 'No notes for this file.' : 'No notes yet: Explain changes writes one for each hunk.'}</p>
          )}
        </div>
        <div className="hunk-actions">
          <Button variant={right(hunk) ? 'secondary' : 'primary'} icon={Check} onClick={() => void looksRight()} kbd="A">
            {right(hunk) ? 'Right · next' : 'Looks right'}
          </Button>
          <Button icon={MessageSquare} onClick={comment} disabled={!comments?.onAdd} kbd="M">Comment</Button>
        </div>
        <div className="xs faint hunk-keys">
          <Kbd>←</Kbd> <Kbd>→</Kbd> hunks · <Kbd>A</Kbd> right · <Kbd>M</Kbd> comment
        </div>
      </aside>
      <div className="hunk-code">
        <DiffView key={hunkKey(hunk) + session.hash + layout} lines={lines.slice(hunk.start, hunk.end)} language={languageOf(hunk.path)} layout={layout} wrap={wrap} anchorPrefix={`h-${hunkKey(hunk)}`} comments={comments} focus={focus} />
      </div>
    </div>
  );
}

/** The hunk the route names; else the one showing the focused line, else the file's first. */
function currentHunk(all: Hunk[], linesOf: Map<string, DiffLine[]>, path: string, n: number, focus?: number): number {
  const named = all.findIndex(h => h.path === path && h.n === n);
  if (named >= 0) return named;
  if (focus) {
    const showing = all.findIndex(h => h.path === path && hunkHasLine(linesOf.get(path) ?? [], h, focus));
    if (showing >= 0) return showing;
  }
  return Math.max(0, all.findIndex(h => h.path === path));
}
