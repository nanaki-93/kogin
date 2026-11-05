// The bar under the diff: what is left of the review (files viewed, checks,
// reviewers) and the last step, Accept & commit, which says what still
// blocks it until it can run.

import { ArrowRight, Lock } from 'lucide-react';
import type { LoadedSession } from '@/api/types';
import { checkSummary, isFileReviewed, proposalReadiness, reviewProgress } from '@/lib/readiness';
import { reviewVerdict } from '@/lib/workflow';
import { plural } from '@/lib/util';
import { Button, cx } from '@/components/ui';
import { AcceptTask } from '@/features/tasks/AcceptTask';

const VERDICT: Record<string, string> = { approve: 'reviewers approved', changes_requested: 'reviewers asked for changes', pending: 'reviewers working', none: 'no review yet' };

export function ReviewBar({ session, current, onNext, onSelect }: { session: LoadedSession; current: string; onNext?: () => void; onSelect: (path: string) => void }) {
  const ready = proposalReadiness(session);
  if (!ready.editable) return null;
  const progress = reviewProgress(session);
  const checks = checkSummary(session.checks, session.checks_outdated);
  const left = progress.total - progress.reviewed;
  return (
    <div className="review-bar" role="region" aria-label="Review progress">
      <span className="strong small">{progress.reviewed} of {progress.total} viewed</span>
      <span className="review-dots">
        {(session.changes ?? []).map(edit => (
          <button key={edit.path} type="button" className={cx('review-dot', isFileReviewed(session, edit.path) && 'is-viewed', edit.path === current && 'is-current')} title={edit.path} aria-label={`${edit.path}${isFileReviewed(session, edit.path) ? ', viewed' : ''}`} onClick={() => onSelect(edit.path)} />
        ))}
      </span>
      <span className="small muted truncate">Checks {checks.label.toLowerCase()} · {VERDICT[reviewVerdict(session.workflow, session.hash)]}</span>
      <span className="spacer" />
      {left && onNext ? <Button size="sm" iconRight={ArrowRight} onClick={onNext}>Next unviewed</Button> : null}
      {ready.canAccept ? <AcceptTask session={session} /> : <span className="review-lock" title={ready.message}><Lock size={12} aria-hidden /> Accept &amp; commit · {blocker(ready.step, left)}</span>}
    </div>
  );
}

function blocker(step: string, left: number): string {
  if (step === 'review') return `${plural(left, 'file')} left`;
  if (step === 'checks') return 'checks first';
  if (step === 'agents') return 'agents not done';
  return 'not ready';
}
