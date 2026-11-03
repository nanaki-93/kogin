// Accept & commit in one step: once every file is viewed and the checks
// pass, one confirmation records your approval of this exact version (with
// optional notes) and commits it on the task's branch.

import { useState } from 'react';
import { GitCommitHorizontal } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { proposalReadiness } from '@/lib/readiness';
import { Button, Input, Textarea } from '@/components/ui';
import { Modal } from '@/components/overlays';

const MAX_MESSAGE = 200;
const MAX_NOTES = 2048;

/** The commit message Accept offers: the task's kind and title. */
export function defaultCommitMessage(session: Pick<LoadedSession, 'kind' | 'title'>): string {
  return `${session.kind === 'fix' ? 'fix' : 'feat'}: ${session.title}`.slice(0, 120);
}

/** The Accept & commit button and its confirmation; disabled until the version can be accepted. */
export function AcceptTask({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const ready = proposalReadiness(session);
  const [open, setOpen] = useState(false);
  const [message, setMessage] = useState(() => defaultCommitMessage(session));
  const [notes, setNotes] = useState('');
  const branch = session.worktree?.branch ?? '';
  const submit = async () => {
    if (await actions.accept(session, message, ready.approved ? '' : notes)) {
      setOpen(false);
      setNotes('');
    }
  };
  return (
    <>
      <Button variant="primary" icon={GitCommitHorizontal} onClick={() => setOpen(true)} disabled={!ready.canAccept || Boolean(busy)}>
        Accept & commit
      </Button>
      <Modal
        open={open}
        onOpenChange={setOpen}
        title="Accept and commit?"
        description={`Commits “${session.title || session.id}” on ${branch}, exactly as reviewed${ready.approved ? '' : ', and records your approval of this version'}. Its files are already in the task's worktree; merge the branch from Branches when you are ready.`}
        footer={
          <>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" form="accept-task" variant="primary" icon={GitCommitHorizontal} disabled={!message.trim() || Boolean(busy)} loading={busy === 'Accepting…'}>
              Accept & commit
            </Button>
          </>
        }
      >
        <form
          id="accept-task"
          className="stack"
          onSubmit={event => {
            event.preventDefault();
            void submit();
          }}
        >
          <label className="field">
            <span className="field-label">Commit message</span>
            <Input value={message} onChange={e => setMessage(e.target.value)} maxLength={MAX_MESSAGE} aria-label="Commit message" autoFocus />
          </label>
          {ready.approved ? null : (
            <label className="field">
              <span className="field-label">Notes <span className="faint">(optional)</span></span>
              <Textarea rows={3} value={notes} onChange={e => setNotes(e.target.value)} maxLength={MAX_NOTES} placeholder="Kept with your approval, such as follow-ups." aria-label="Approval notes" />
            </label>
          )}
        </form>
      </Modal>
    </>
  );
}
