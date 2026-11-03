// The human's last word on a review: overriding the model reviewers'
// rejection with a reason, and the notes kept with an approval.

import { useState } from 'react';
import { Gavel, NotebookPen } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { Button, Callout, Textarea } from '@/components/ui';
import { Modal } from '@/components/overlays';
import { ReviewItems } from './ReviewItems';

const MAX_REASON = 1024;

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
        You have the last word: override them with a reason, then review and accept this version yourself.
      </Callout>
      <Button icon={Gavel} onClick={() => setOpen(true)} disabled={Boolean(busy)}>
        Override reviewers…
      </Button>
      <Modal
        open={open}
        onOpenChange={setOpen}
        title="Override the reviewers?"
        description="The reviewers' verdict stays on record. This version then waits for your review: view every file and keep checks passing, then Accept & commit."
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
