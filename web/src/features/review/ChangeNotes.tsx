// What the change fixes, read hunk by hunk: a line over the file's diff on
// what its change fixes, and above each hunk the issue it serves, the wrong
// behavior it removes and how. The review model wrote it; it is advice, never
// review evidence.

import { useState } from 'react';
import { RefreshCw, Sparkles } from 'lucide-react';
import type { DiffLine, LoadedSession, WalkthroughFile, WalkthroughNote } from '@/api/types';
import { useActions } from '@/app/actions';
import { noteStart } from '@/lib/diff';
import { HUNK_KINDS } from '@/lib/hunks';
import { workflowActive } from '@/lib/readiness';
import { Badge, Button, IconButton, Spinner } from '@/components/ui';
import { DocText } from '@/features/docs/DocText';
import type { DiffNote } from './DiffView';

/** The walkthrough's entry for a file, if the change was explained. */
export function walkthroughFile(session: LoadedSession, path: string): WalkthroughFile | undefined {
  const walkthrough = session.walkthrough;
  if (walkthrough?.status !== 'ready') return undefined;
  return walkthrough.files?.find(file => file.path === path);
}

/**
 * Splits a file's notes into those shown above their hunk and those whose
 * line the diff lacks (no line, or a line the diff does not show).
 */
export function placeNotes(session: LoadedSession, file: WalkthroughFile | undefined, lines: DiffLine[]): { placed: DiffNote[]; unplaced: WalkthroughNote[] } {
  const placed: DiffNote[] = [];
  const unplaced: WalkthroughNote[] = [];
  for (const note of file?.notes ?? []) {
    if (note.line && noteStart(lines, note.line) >= 0) placed.push({ line: note.line, content: <HunkNote note={note} goal={goalLabel(session, note)} /> });
    else unplaced.push(note);
  }
  return { placed, unplaced };
}

/** Over the diff: what the file's change fixes, and asking for the notes. */
export function ChangeNotes({ session, path, unplaced }: { session: LoadedSession; path: string; unplaced: WalkthroughNote[] }) {
  const actions = useActions();
  const [pending, setPending] = useState(false);
  const walkthrough = session.walkthrough;
  const running = workflowActive(session);
  const explain = async () => {
    setPending(true);
    try {
      await actions.explainChange(session);
    } finally {
      setPending(false);
    }
  };
  const again = walkthrough && !running && !pending ? <IconButton icon={RefreshCw} label="Explain again" size="sm" onClick={() => void explain()} /> : null;

  let body;
  if (pending) {
    body = <div className="xs muted row"><Spinner size={12} label="Explaining" /> Explaining the change…</div>;
  } else if (!walkthrough) {
    body = running
      ? <span className="xs muted">Notes come with the review.</span>
      : <Button size="sm" icon={Sparkles} onClick={() => void explain()}>Explain changes</Button>;
  } else if (walkthrough.status === 'failed') {
    body = (
      <div className="row">
        <span className="xs tone-danger">Not explained{walkthrough.reason ? `: ${walkthrough.reason}` : '.'}</span>
        {!running ? <Button size="sm" icon={RefreshCw} onClick={() => void explain()}>Try again</Button> : null}
      </div>
    );
  } else {
    const file = walkthroughFile(session, path);
    body = (
      <>
        {walkthrough.proposal_hash !== session.hash ? <p className="xs tone-warning">For an earlier version of the change.</p> : null}
        {file?.summary ? <p className="change-notes-summary small"><DocText text={file.summary} /></p> : null}
        {!file?.notes?.length ? <p className="xs muted">No notes for this file.</p> : null}
        {unplaced.map((note, index) => <HunkNote key={index} note={note} goal={goalLabel(session, note)} />)}
      </>
    );
  }

  return (
    <section className="change-notes" aria-label="What it fixes">
      <div className="change-notes-body">{body}</div>
      <Badge tone="accent" icon={Sparkles} title="Written by the review model from the diff. Check the code before relying on it.">AI</Badge>
      {again}
    </section>
  );
}

/** One hunk's note: its part in the story, the goal it serves, the wrong behavior it removes, how. */
export function HunkNote({ note, goal, hideKind }: { note: WalkthroughNote; goal: string; hideKind?: boolean }) {
  const kind = hideKind ? undefined : HUNK_KINDS[note.kind ?? ''];
  return (
    <div className="hunk-note">
      <div className="row">
        <span className={note.goal ? 'xs hunk-note-goal truncate' : 'xs faint truncate'} title={goal}>{goal}</span>
        {kind ? <Badge tone={kind.tone}>{kind.label}</Badge> : null}
      </div>
      <div className="small"><DocText text={note.problem} /></div>
      {note.how ? <div className="xs muted">How: <DocText text={note.how} /></div> : null}
      {note.concepts?.length ? (
        <div className="chips">
          {note.concepts.map(concept => (
            <span key={concept} className="chip" title="A concept this hunk is a worked example of; accepted tasks collect them in Docs → Learning log">{concept}</span>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** What a note serves: "Issue 2 · Salt discarded", "Criterion 1" or a change another hunk needs. */
export function goalLabel(session: Pick<LoadedSession, 'issues' | 'acceptance_criteria'>, note: WalkthroughNote): string {
  const n = note.goal ?? 0;
  if (n <= 0) return 'Needed by the change';
  const issue = session.issues?.at(n - 1);
  if (session.issues?.length) return issue ? `Issue ${n} · ${issue.title}` : `Issue ${n}`;
  return session.acceptance_criteria.at(n - 1) ? `Criterion ${n} · ${session.acceptance_criteria.at(n - 1)}` : `Criterion ${n}`;
}
