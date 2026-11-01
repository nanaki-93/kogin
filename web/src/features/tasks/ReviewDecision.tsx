// The human's last word on a review: overriding the model reviewers'
// rejection with a reason, and approving a version with notes.

import { useState } from 'react';
import { Check, Gavel, NotebookPen } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { Button, Callout, Textarea } from '@/components/ui';
import { Modal } from '@/components/overlays';
import { ReviewItems } from './ReviewItems';

const MAX_REASON = 1024;
const MAX_NOTES = 2048;

/** Offered when the reviewers rejected this exact version and the human may disagree. */
export function OverrideReview({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState('');
  const findings = session.workflow?.review?.findings ?? [];
  const submit = async () => {
    if (await actions.overrideReview(session, reason)) {
      setOpen(false);
      setReason('');
    }
  };
  return (
    <section className="rail-section">
      <Callout tone="warning" icon={Gavel} title="Reviewers requested changes">
        You have the last word: override them with a reason, then approve this version yourself.
      </Callout>
      <Button icon={Gavel} onClick={() => setOpen(true)} disabled={Boolean(busy)}>
        Override reviewers…
      </Button>
      <Modal
        open={open}
        onOpenChange={setOpen}
        title="Override the reviewers?"
        description="The reviewers' verdict stays on record. This version then waits for your review: view every file, keep checks passing and approve it before Apply."
        footer={
          <>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button variant="primary" icon={Gavel} onClick={() => void submit()} disabled={!reason.trim() || Boolean(busy)} loading={busy === 'Overriding…'}>
              Override reviewers
            </Button>
          </>
        }
      >
        <div className="stack">
          {findings.length ? (
            <div className="small">
              <div className="label-row">Blocking findings</div>
              <ReviewItems items={findings} session={session} links={false} />
            </div>
          ) : null}
          <label className="field">
            <span className="field-label">Why is this version acceptable?</span>
            <Textarea rows={3} value={reason} onChange={e => setReason(e.target.value)} maxLength={MAX_REASON} placeholder="For example: the dependency direction is intended for this package." aria-label="Override reason" autoFocus />
          </label>
        </div>
      </Modal>
    </section>
  );
}

/** Approve with optional notes that stay with this approval. */
export function ApproveWithNotes({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const [notes, setNotes] = useState('');
  const [writing, setWriting] = useState(false);
  return (
    <>
      {writing ? (
        <Textarea rows={3} value={notes} onChange={e => setNotes(e.target.value)} maxLength={MAX_NOTES} placeholder="Notes kept with your approval, such as follow-ups." aria-label="Approval notes" autoFocus />
      ) : (
        <Button variant="ghost" size="sm" icon={NotebookPen} onClick={() => setWriting(true)}>
          Add notes
        </Button>
      )}
      <Button variant="primary" icon={Check} onClick={() => void actions.approve(session, notes)} disabled={Boolean(busy)} loading={busy === 'Approving…'}>
        {notes.trim() ? 'Approve with notes' : 'Approve this version'}
      </Button>
    </>
  );
}

/** The notes recorded with the approval of this exact version. */
export function ApprovalNotes({ session }: { session: LoadedSession }) {
  if (!session.review_notes || session.reviewed_hash !== session.hash) return null;
  return (
    <div className="approval-notes small">
      <div className="label-row">
        <NotebookPen size={13} aria-hidden /> Your notes
      </div>
      <p className="prose">{session.review_notes}</p>
    </div>
  );
}
