// The plan the investigation proposed for a task: the problem, its root
// cause with cited lines, the approach, the alternatives it rejected and,
// for a task that fixes issues, a verdict per issue. While the run waits at
// awaiting_plan_review, approving it starts the agents and a redirect runs
// the investigation again with a note.

import { useState } from 'react';
import { Check, CircleSlash, CornerDownLeft, Map as MapIcon } from 'lucide-react';
import type { LoadedSession, PlanIssueVerdict } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { navigate } from '@/lib/router';
import { timeAgo } from '@/lib/util';
import { Badge, Button, Textarea } from '@/components/ui';
import { FileIcon } from '@/components/domain';

export function PlanReview({ session }: { session: LoadedSession }) {
  const plan = session.plan;
  if (!plan || plan.source !== 'investigator') return null;
  const waiting = session.workflow?.status === 'awaiting_plan_review';
  const approved = Boolean(plan.hash) && plan.approved_hash === plan.hash;
  return (
    <section className="card" aria-label="Proposed plan">
      <div className="card-head">
        <MapIcon size={15} aria-hidden />
        <span className="strong">Proposed plan</span>
        <Badge tone={approved ? 'success' : waiting ? 'accent' : 'neutral'}>{approved ? 'Approved' : waiting ? 'Needs your decision' : 'Superseded'}</Badge>
        <span className="spacer" />
        {plan.model ? <span className="xs faint mono">{plan.model}</span> : null}
        {plan.investigated_at ? <span className="xs faint">{timeAgo(plan.investigated_at)}</span> : null}
      </div>
      <div className="card-body stack">
        <div>
          <div className="label-row">Problem</div>
          <p className="prose small">{plan.problem}</p>
        </div>
        {plan.root_cause ? (
          <div>
            <div className="label-row">Root cause</div>
            <p className="prose small">{plan.root_cause}</p>
            {(plan.citations ?? []).length ? (
              <ul className="issue-steps" aria-label="Cited lines">
                {(plan.citations ?? []).map(citation => (
                  <li key={citation.path + citation.start_line} className="issue-step scope-grant">
                    <div className="issue-step-head">
                      <button
                        type="button"
                        className="chip chip-file"
                        onClick={() => navigate('/code', { file: citation.path, line: citation.start_line ? String(citation.start_line) : undefined, wt: session.checkout.worktreePath || undefined })}
                        title={citation.quote}
                      >
                        <FileIcon path={citation.path} size={12} />
                        {citation.path.slice(citation.path.lastIndexOf('/') + 1)}:{citation.start_line}
                        {citation.end_line && citation.end_line !== citation.start_line ? `–${citation.end_line}` : ''}
                      </button>
                      <span className="xs muted truncate" title={citation.observation}>
                        {citation.observation}
                      </span>
                    </div>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : null}
        <div>
          <div className="label-row">Approach</div>
          <p className="prose small">{plan.approach}</p>
          {(plan.touches ?? []).length ? (
            <div className="chips">
              {(plan.touches ?? []).map(touch => (
                <button key={touch.path} type="button" className="chip chip-file" title={touch.why} onClick={() => navigate('/code', { file: touch.path, wt: session.checkout.worktreePath || undefined })}>
                  <FileIcon path={touch.path} size={12} />
                  {touch.path}
                </button>
              ))}
            </div>
          ) : null}
        </div>
        {(plan.issue_verdicts ?? []).length ? <PlanVerdicts session={session} verdicts={plan.issue_verdicts!} /> : null}
        {(plan.alternatives ?? []).length ? (
          <div>
            <div className="label-row">Rejected alternatives</div>
            <ul className="issue-steps" aria-label="Rejected alternatives">
              {(plan.alternatives ?? []).map(alternative => (
                <li key={alternative.approach} className="issue-step scope-grant">
                  <p className="small">{alternative.approach}</p>
                  <p className="xs muted">Why not: {alternative.why_not}</p>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {(plan.risks ?? []).length ? (
          <div>
            <div className="label-row">Risks</div>
            <ul className="criteria-read">
              {(plan.risks ?? []).map(risk => (
                <li key={risk} className="small">{risk}</li>
              ))}
            </ul>
          </div>
        ) : null}
      </div>
      {waiting ? <PlanDecision session={session} /> : null}
    </section>
  );
}

/** What the investigation found per linked issue, before any code. */
function PlanVerdicts({ session, verdicts }: { session: LoadedSession; verdicts: PlanIssueVerdict[] }) {
  const issues = session.issues ?? [];
  const titled = (findingId: string) => issues.find(issue => issue.finding_id === findingId)?.title ?? findingId;
  const label: Record<PlanIssueVerdict['verdict'], { text: string; tone: 'neutral' | 'success' | 'warning' | 'info' }> = {
    holds: { text: 'Holds', tone: 'info' },
    already_fixed: { text: 'Already fixed', tone: 'success' },
    does_not_hold: { text: 'Does not hold', tone: 'neutral' },
    needs_own_task: { text: 'Needs its own task', tone: 'warning' },
  };
  return (
    <div>
      <div className="label-row">Issue verdicts</div>
      <ul className="issue-steps" aria-label="Issue verdicts">
        {verdicts.map(verdict => (
          <li key={verdict.finding_id} className="issue-step">
            <div className="issue-step-head">
              <span className="small truncate" title={titled(verdict.finding_id)}>
                {titled(verdict.finding_id)}
              </span>
              <span className="spacer" />
              <Badge tone={label[verdict.verdict]?.tone ?? 'neutral'}>{label[verdict.verdict]?.text ?? verdict.verdict}</Badge>
            </div>
            {verdict.note ? <p className="xs muted">{verdict.note}</p> : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Approve the plan (starting the agents) or redirect it with a note. */
function PlanDecision({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = Boolean(useBusy());
  const [redirecting, setRedirecting] = useState(false);
  const [note, setNote] = useState('');
  const redirect = async () => {
    if (await actions.startAgents(session, { planRedirect: note.trim() })) {
      setRedirecting(false);
      setNote('');
    }
  };
  return (
    <div className="card-foot stack">
      {redirecting ? (
        <>
          <Textarea rows={2} autoFocus placeholder="What should the plan do differently? The investigation runs again with this note." value={note} onChange={e => setNote(e.target.value)} aria-label="Redirect note" />
          <div className="row">
            <Button size="sm" variant="ghost" icon={CircleSlash} onClick={() => setRedirecting(false)} disabled={busy}>
              Cancel
            </Button>
            <span className="spacer" />
            <Button size="sm" variant="secondary" icon={CornerDownLeft} onClick={() => void redirect()} disabled={!note.trim() || busy}>
              Re-investigate
            </Button>
          </div>
        </>
      ) : (
        <div className="row">
          <span className="xs muted">No code yet: your decision steers the agents while it still matters.</span>
          <span className="spacer" />
          <Button size="sm" variant="secondary" onClick={() => setRedirecting(true)} disabled={busy}>
            Redirect…
          </Button>
          <Button size="sm" variant="primary" icon={Check} onClick={() => void actions.startAgents(session, { approvePlan: true })} disabled={busy}>
            Approve & run agents
          </Button>
        </div>
      )}
    </div>
  );
}
