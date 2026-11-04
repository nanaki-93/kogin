// The walkthrough beside a file's diff: what the file's change does and,
// hunk by hunk, what changed and why. A note opens its line in the diff.
// The review model wrote it; it is advice, never review evidence.

import { useState } from 'react';
import { RefreshCw, Sparkles } from 'lucide-react';
import type { LoadedSession, WalkthroughFile } from '@/api/types';
import { useActions } from '@/app/actions';
import { workflowActive } from '@/lib/readiness';
import { Badge, Button, IconButton, Spinner } from '@/components/ui';
import { DocText } from '@/features/docs/DocText';

export function ChangeNotes({ session, path, onLine }: { session: LoadedSession; path: string; onLine: (line: number) => void }) {
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
  const again = !running && !pending ? <IconButton icon={RefreshCw} label="Explain again" size="sm" onClick={() => void explain()} /> : null;

  let body;
  if (pending) {
    body = <div className="xs muted row"><Spinner size={12} label="Explaining" /> Explaining the change…</div>;
  } else if (!walkthrough) {
    body = running
      ? <p className="xs muted">Notes come with the review.</p>
      : <Button size="sm" icon={Sparkles} onClick={() => void explain()}>Explain changes</Button>;
  } else if (walkthrough.status === 'failed') {
    body = (
      <div className="stack">
        <p className="xs tone-danger">Not explained{walkthrough.reason ? `: ${walkthrough.reason}` : '.'}</p>
        {!running ? <Button size="sm" icon={RefreshCw} onClick={() => void explain()}>Try again</Button> : null}
      </div>
    );
  } else {
    const file = walkthrough.files?.find(entry => entry.path === path);
    body = (
      <>
        {walkthrough.proposal_hash !== session.hash ? <p className="xs tone-warning">For an earlier version of the change.</p> : null}
        {file ? <FileNotes file={file} onLine={onLine} /> : <p className="xs muted">No notes for this file.</p>}
      </>
    );
  }

  return (
    <aside className="change-notes" aria-label="Why it changed">
      <div className="change-notes-head">
        <span className="strong small grow">Why it changed</span>
        <Badge tone="accent" icon={Sparkles} title="Written by the review model from the diff. Check the code before relying on it.">AI</Badge>
        {walkthrough ? again : null}
      </div>
      {body}
    </aside>
  );
}

function FileNotes({ file, onLine }: { file: WalkthroughFile; onLine: (line: number) => void }) {
  return (
    <>
      {file.summary ? <p className="change-notes-summary small"><DocText text={file.summary} /></p> : null}
      <ol className="change-notes-list">
        {(file.notes ?? []).map((note, index) => (
          <li key={index}>
            {note.line ? (
              <button type="button" className="link-btn mono xs change-note-line" onClick={() => onLine(note.line!)} title={`Show line ${note.line} in the diff`}>
                L{note.line}
              </button>
            ) : <span className="mono xs faint change-note-line">·</span>}
            <div className="change-note-body">
              <div className="small"><DocText text={note.what} /></div>
              {note.why ? <div className="xs muted"><DocText text={note.why} /></div> : null}
            </div>
          </li>
        ))}
      </ol>
    </>
  );
}
