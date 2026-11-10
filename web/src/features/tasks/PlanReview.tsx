// The plan the investigation proposed for a task: the problem, its root
// cause with cited lines, the approach, the alternatives it rejected and,
// for a task that fixes issues, a verdict per issue. While the run waits at
// awaiting_plan_review, approving it starts the agents and a redirect runs
// the investigation again with a note.

import { useState } from 'react';
import { Check, CircleSlash, CornerDownLeft, Map as MapIcon } from 'lucide-react';
import type { LoadedSession, PlanIssueVerdict } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { issueTitle, planAwaitsDecision, planFindsNothingToFix } from '@/lib/readiness';
import { CloseTask } from './CloseTask';
import { navigate } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { Badge, Button, Textarea } from '@/components/ui';
import { FileIcon } from '@/components/domain';

export function PlanReview({ session }: { session: LoadedSession }) {
  const plan = session.plan;
  const [redirecting, setRedirecting] = useState(false);
  if (!plan || plan.source !== 'investigator') return null;
  const waiting = planAwaitsDecision(session);
  const approved = Boolean(plan.hash) && plan.approved_hash === plan.hash;
  // No linked issue needs a change: the decision is to close, not to approve.
  const nothing = planFindsNothingToFix(session);
  return (
    <section className="card" aria-label="Proposed plan">
      <div className="card-head">
        <MapIcon size={15} aria-hidden />
        <span className="strong">Proposed plan</span>
        <Badge tone={approved ? 'success' : waiting && !nothing ? 'accent' : 'neutral'}>{nothing ? 'Nothing to fix' : approved ? 'Approved' : waiting ? 'Needs your decision' : 'Superseded'}</Badge>
        <span className="spacer" />
        {plan.model && !waiting ? <span className="xs faint mono">{plan.model}</span> : null}
        {plan.investigated_at && !waiting ? <span className="xs faint">{timeAgo(plan.investigated_at)}</span> : null}
        {waiting ? (
          <>
            <Button size="sm" variant="secondary" onClick={() => setRedirecting(true)} disabled={redirecting}>
              {nothing ? 'I think it’s real…' : 'Redirect…'}
            </Button>
            {nothing ? <CloseTask session={session} variant="primary" /> : <ApprovePlan session={session} size="sm" />}
          </>
        ) : null}
      </div>
      {waiting && redirecting ? <RedirectPlan session={session} onDone={() => setRedirecting(false)} /> : null}
      <div className="card-body stack plan-body">
        <div>
          <div className="label-row">Problem</div>
          <p className="prose small">{plan.problem}</p>
        </div>
        <div>
          <div className="label-row">Approach</div>
          <ApproachSteps approach={plan.approach ?? ''} />
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
        {plan.root_cause || plan.alternatives?.length || plan.risks?.length ? (
          <details className="plan-why">
            <summary className="small">
              Why this plan
              <span className="xs muted"> · {[plan.root_cause ? 'root cause' : '', plan.alternatives?.length ? plural(plan.alternatives.length, 'alternative') : '', plan.risks?.length ? plural(plan.risks.length, 'risk') : ''].filter(Boolean).join(', ')}</span>
            </summary>
            <div className="stack">
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
              {(plan.alternatives ?? []).length ? (
                <div>
                  <div className="label-row">Rejected alternatives</div>
                  <ul className="issue-steps" aria-label="Rejected alternatives">
                    {(plan.alternatives ?? []).map(alternative => (
                      <li key={alternative.approach} className="issue-step scope-grant">
                        <span className="small">{alternative.approach}</span>
                        <span className="xs muted"> — {alternative.why_not}</span>
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
          </details>
        ) : null}
      </div>
    </section>
  );
}

/** The approach as its numbered steps, one per line; a single line reads as prose. */
function ApproachSteps({ approach }: { approach: string }) {
  const steps = approach.split('\n').map(line => line.replace(/^\s*\d+[.)]\s*/, '').trim()).filter(Boolean);
  if (steps.length <= 1) return <p className="prose small">{approach}</p>;
  return (
    <ol className="criteria-read plan-steps">
      {steps.map((step, index) => (
        <li key={index} className="small">{step}</li>
      ))}
    </ol>
  );
}

/** Approves the displayed plan and starts the agents from it. */
export function ApprovePlan({ session, size, variant = 'primary' }: { session: LoadedSession; size?: 'sm'; variant?: 'primary' | 'secondary' }) {
  const actions = useActions();
  const busy = Boolean(useBusy());
  return (
    <Button size={size} variant={variant} icon={Check} onClick={() => void actions.startAgents(session, { approvePlan: true })} disabled={busy} title="Approves this exact plan and starts the agents from it; nothing is written until then">
      Approve & run agents
    </Button>
  );
}

/** What the investigation found per linked issue, before any code. */
function PlanVerdicts({ session, verdicts }: { session: LoadedSession; verdicts: PlanIssueVerdict[] }) {
  const issues = session.issues ?? [];
  // A task that fixes one issue without steps is titled after it.
  const titled = (findingId: string) => issues.find(issue => issue.finding_id === findingId)?.title ?? issueTitle(session.title);
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

/** A note for the investigation to run again with; it replaces the plan. */
function RedirectPlan({ session, onDone }: { session: LoadedSession; onDone: () => void }) {
  const actions = useActions();
  const busy = Boolean(useBusy());
  const [note, setNote] = useState('');
  const redirect = async () => {
    if (await actions.startAgents(session, { planRedirect: note.trim() })) onDone();
  };
  return (
    <div className="card-body stack plan-redirect">
      <Textarea rows={2} autoFocus placeholder={planFindsNothingToFix(session) ? 'Why do you think the issue is real? The investigation runs again with this note.' : 'What should the plan do differently? The investigation runs again with this note.'} value={note} onChange={e => setNote(e.target.value)} aria-label="Redirect note" />
      <div className="row">
        <Button size="sm" variant="ghost" icon={CircleSlash} onClick={onDone} disabled={busy}>
          Cancel
        </Button>
        <span className="spacer" />
        <Button size="sm" variant="secondary" icon={CornerDownLeft} onClick={() => void redirect()} disabled={!note.trim() || busy}>
          Re-investigate
        </Button>
      </div>
    </div>
  );
}
