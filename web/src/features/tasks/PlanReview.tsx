// The plan the investigation proposed for a task, as one decision: what is
// wrong in a plain sentence, the lines that show it as code with the step
// that changes them, and the approach. The reasoning (root cause,
// alternatives, risks, files) folds below. While the run waits at
// awaiting_plan_review the task header approves or redirects; a plan that
// finds nothing to fix is closed instead.

import { useMemo, useState, type ReactNode } from 'react';
import { Check, CircleSlash, CornerDownLeft, Map as MapIcon } from 'lucide-react';
import type { LoadedSession, OwnPlan, PlanEvidence, PlanIssueVerdict } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { highlightLines } from '@/lib/highlight';
import { issueTitle, ownPlanFirst, planAwaitsDecision, planFindsNothingToFix } from '@/lib/readiness';
import { navigate } from '@/lib/router';
import { basename, languageOf, plural, timeAgo } from '@/lib/util';
import { Badge, Button, Segmented, Textarea, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';
import { DocText } from '@/features/docs/DocText';

export function PlanReview({ session, redirecting, onRedirectDone }: { session: LoadedSession; redirecting: boolean; onRedirectDone: () => void }) {
  const plan = session.plan;
  if (!plan || plan.source !== 'investigator') return null;
  const waiting = planAwaitsDecision(session);
  const approved = Boolean(plan.hash) && plan.approved_hash === plan.hash;
  // No linked issue needs a change: the decision is to close, not to approve.
  const nothing = planFindsNothingToFix(session);
  const citations = plan.citations ?? [];
  const shown = citations.filter(citation => citation.excerpt);
  const steps = approachSteps(plan.approach ?? '');
  const verdicts = plan.issue_verdicts ?? [];
  // Your plan first: the agent's stays out of sight until yours is written or skipped.
  if (ownPlanFirst(session)) return <YourPlanFirst session={session} />;
  const own = session.own_plan;
  const compared = Boolean(own && !own.skipped);
  return (
    <section className="card plan-card" aria-label="Proposed plan">
      <div className="card-head">
        <MapIcon size={15} aria-hidden />
        <span className="strong">Plan</span>
        <Badge tone={approved ? 'success' : waiting && !nothing ? 'accent' : 'neutral'}>{nothing ? 'Nothing to fix' : approved ? 'Approved' : waiting ? 'Needs your decision' : 'Superseded'}</Badge>
        <span className="spacer" />
        {plan.model ? <span className="xs faint mono">{plan.model}</span> : null}
        {plan.investigated_at ? <span className="xs faint">{timeAgo(plan.investigated_at)}</span> : null}
      </div>
      {waiting && redirecting ? <RedirectPlan session={session} onDone={onRedirectDone} /> : null}
      <div className="card-body stack plan-body">
        {compared ? (
          <ComparePlans session={session} steps={steps} />
        ) : (
          <p className="plan-claim">
            <DocText text={plan.claim || plan.problem || ''} shorten />
          </p>
        )}
        {shown.length ? (
          <div className="stack-sm" aria-label="The lines that show it">
            {shown.map(citation => (
              <CitedLines key={`${citation.path}:${citation.excerpt_line}`} session={session} citation={citation} />
            ))}
          </div>
        ) : null}
        {nothing || compared ? null : (
          <div>
            <div className="label-row">Approach</div>
            {steps.length > 1 ? (
              <ol className="criteria-read plan-steps">
                {steps.map((step, index) => (
                  <li key={index}>
                    <DocText text={step} shorten />
                  </li>
                ))}
              </ol>
            ) : (
              <p className="prose">
                <DocText text={steps[0] ?? ''} shorten />
              </p>
            )}
          </div>
        )}
        {verdicts.length ? <PlanVerdicts session={session} verdicts={verdicts} /> : null}
        <PlanReasoning session={session} folded={citations.filter(citation => !citation.excerpt)} />
        {own?.skipped ? <p className="xs faint">You read this plan without writing your own first.</p> : null}
      </div>
    </section>
  );
}

/**
 * Before the agent's plan is shown: what you think is wrong, what you would
 * do and what could break. It is kept as written and set beside the agent's.
 */
function YourPlanFirst({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = Boolean(useBusy());
  const [wrong, setWrong] = useState('');
  const [approach, setApproach] = useState('');
  const [risk, setRisk] = useState('');
  const ready = wrong.trim() && approach.trim();
  return (
    <section className="card plan-card" aria-label="Your plan first">
      <div className="card-head">
        <MapIcon size={15} aria-hidden />
        <span className="strong">Your plan first</span>
        <Badge tone="accent">The agent’s plan is ready</Badge>
      </div>
      <form
        className="card-body stack plan-own"
        onSubmit={event => {
          event.preventDefault();
          if (ready) void actions.writeOwnPlan(session, { wrong: wrong.trim(), approach: approach.trim(), risk: risk.trim() || undefined });
        }}
      >
        <p className="small muted">Before you read it, write your own from the brief and the code. It is kept as you wrote it and shown beside the agent’s; where the two differ is what this task can teach you.</p>
        <label className="stack-xs">
          <span className="label-row">What is wrong?</span>
          <Textarea rows={2} autoFocus value={wrong} onChange={e => setWrong(e.target.value)} maxLength={600} placeholder="The cause, as far as you can tell: which code, doing what" />
        </label>
        <label className="stack-xs">
          <span className="label-row">What would you do?</span>
          <Textarea rows={2} value={approach} onChange={e => setApproach(e.target.value)} maxLength={600} placeholder="The smallest change that fixes it" />
        </label>
        <label className="stack-xs">
          <span className="label-row">What could break? <span className="faint">(optional)</span></span>
          <Textarea rows={1} value={risk} onChange={e => setRisk(e.target.value)} maxLength={600} placeholder="Who relies on the current behavior" />
        </label>
        <div className="row">
          <Button type="submit" variant="primary" disabled={!ready || busy}>
            Show the agent’s plan
          </Button>
          <Button variant="ghost" onClick={() => void actions.writeOwnPlan(session, { skip: true })} disabled={busy} title="Recorded as skipped: nothing is compared for this task">
            Skip this time
          </Button>
        </div>
      </form>
    </section>
  );
}

const MATCHES: { value: NonNullable<OwnPlan['match']>; label: string }[] = [
  { value: 'same', label: 'Same cause' },
  { value: 'partly', label: 'Partly' },
  { value: 'missed', label: 'Missed it' },
];

/** Your plan and the agent's, answer by answer, and your own mark of how close you were. */
function ComparePlans({ session, steps }: { session: LoadedSession; steps: string[] }) {
  const actions = useActions();
  const own = session.own_plan!;
  const plan = session.plan!;
  const rows: { label: string; yours: string; theirs: ReactNode }[] = [
    { label: 'What is wrong', yours: own.wrong ?? '', theirs: <DocText text={plan.claim || plan.problem || ''} shorten /> },
    {
      label: 'What to do',
      yours: own.approach ?? '',
      theirs: steps.length > 1 ? (
        <ol className="criteria-read plan-steps">
          {steps.map((step, index) => (
            <li key={index}>
              <DocText text={step} shorten />
            </li>
          ))}
        </ol>
      ) : <DocText text={steps[0] ?? ''} shorten />,
    },
    {
      label: 'What could break',
      yours: own.risk ?? '',
      theirs: (plan.risks ?? []).length ? (plan.risks ?? []).map((risk, index) => <div key={index}><DocText text={risk} shorten /></div>) : <span className="faint">Nothing named</span>,
    },
  ];
  return (
    <div className="plan-compare" role="table" aria-label="Your plan and the agent’s">
      <div className="plan-compare-row plan-compare-head" role="row">
        <span role="columnheader" />
        <span role="columnheader">You wrote, before seeing it</span>
        <span role="columnheader">The agent’s plan</span>
      </div>
      {rows.map(row => (
        <div key={row.label} className="plan-compare-row" role="row">
          <span className="label-row" role="rowheader">{row.label}</span>
          <div role="cell">{row.yours || <span className="faint">Left empty</span>}</div>
          <div role="cell">{row.theirs}</div>
        </div>
      ))}
      <div className="plan-compare-mark">
        <span className="small muted">How close were you? Your call; change it once you have seen the fix.</span>
        <Segmented label="How close your plan was" value={(own.match ?? '') as NonNullable<OwnPlan['match']>} onChange={match => void actions.writeOwnPlan(session, { match })} options={MATCHES} />
      </div>
    </div>
  );
}

/** The cited lines as code: where they are, what the investigation says of them, and the step that changes them. */
function CitedLines({ session, citation }: { session: LoadedSession; citation: PlanEvidence }) {
  const first = citation.excerpt_line ?? citation.start_line ?? 1;
  const html = useMemo(() => highlightLines(dedent(citation.excerpt ?? ''), languageOf(citation.path)), [citation.excerpt, citation.path]);
  const last = first + html.length - 1;
  const effect = citation.effect === 'removed' ? 'removes these lines' : citation.effect === 'changed' ? 'changes these lines' : 'works here';
  return (
    <figure className={cx('plan-code', citation.effect && `is-${citation.effect}`)}>
      <figcaption>
        <button
          type="button"
          className="chip chip-file"
          title={`Open ${citation.path} at line ${first}`}
          onClick={() => navigate('/code', { file: citation.path, line: String(first), wt: session.checkout.worktreePath || undefined })}
        >
          <FileIcon path={citation.path} size={12} />
          {basename(citation.path)}:{first}
          {last !== first ? `–${last}` : ''}
        </button>
        <span className="plan-code-note">
          <DocText text={citation.observation ?? ''} shorten />
        </span>
        {citation.step ? <span className={cx('plan-step-tag', citation.effect && `is-${citation.effect}`)}>Step {citation.step} {effect}</span> : null}
      </figcaption>
      <pre className="plan-code-lines">
        {html.map((line, index) => (
          <span key={index} className="plan-code-line">
            <span className="ln">{first + index}</span>
            <span className="sign" aria-hidden>{citation.effect === 'removed' ? '−' : citation.effect === 'changed' ? '~' : ' '}</span>
            <span className="code-text" dangerouslySetInnerHTML={{ __html: line || ' ' }} />
          </span>
        ))}
      </pre>
    </figure>
  );
}

/** Why the plan is this plan: the problem in the code's terms, its root cause, the alternatives it rejected, the risks and the files. */
function PlanReasoning({ session, folded }: { session: LoadedSession; folded: PlanEvidence[] }) {
  const plan = session.plan!;
  const holds = [
    plan.root_cause ? 'root cause' : '',
    plan.alternatives?.length ? plural(plan.alternatives.length, 'alternative') : '',
    plan.risks?.length ? plural(plan.risks.length, 'risk') : '',
    plan.touches?.length ? plural(plan.touches.length, 'file') : '',
  ].filter(Boolean);
  if (!holds.length && !folded.length) return null;
  return (
    <details className="plan-why">
      <summary className="small">
        Why this plan
        <span className="xs muted"> · {holds.join(', ')}</span>
      </summary>
      <div className="stack">
        {plan.claim && plan.problem ? (
          <div>
            <div className="label-row">In the code</div>
            <p className="prose small">
              <DocText text={plan.problem} shorten />
            </p>
          </div>
        ) : null}
        {plan.root_cause ? (
          <div>
            <div className="label-row">Root cause</div>
            <p className="prose small">
              <DocText text={plan.root_cause} shorten />
            </p>
          </div>
        ) : null}
        {folded.length ? (
          <ul className="issue-steps" aria-label="Cited lines">
            {folded.map(citation => (
              <li key={citation.path + citation.start_line} className="issue-step scope-grant">
                <div className="issue-step-head">
                  <button
                    type="button"
                    className="chip chip-file"
                    onClick={() => navigate('/code', { file: citation.path, line: citation.start_line ? String(citation.start_line) : undefined, wt: session.checkout.worktreePath || undefined })}
                    title={citation.quote}
                  >
                    <FileIcon path={citation.path} size={12} />
                    {basename(citation.path)}:{citation.start_line}
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
        {(plan.alternatives ?? []).length ? (
          <div>
            <div className="label-row">Rejected alternatives</div>
            <ul className="issue-steps" aria-label="Rejected alternatives">
              {(plan.alternatives ?? []).map(alternative => (
                <li key={alternative.approach} className="issue-step scope-grant">
                  <span className="small">
                    <DocText text={alternative.approach} shorten />
                  </span>
                  <span className="xs muted">
                    {' '}— <DocText text={alternative.why_not} shorten />
                  </span>
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
                <li key={risk} className="small">
                  <DocText text={risk} shorten />
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {(plan.touches ?? []).length ? (
          <div>
            <div className="label-row">Files it changes</div>
            <ul className="issue-steps" aria-label="Files it changes">
              {(plan.touches ?? []).map(touch => (
                <li key={touch.path} className="issue-step scope-grant">
                  <div className="issue-step-head">
                    <button type="button" className="chip chip-file" title={`Open ${touch.path}`} onClick={() => navigate('/code', { file: touch.path, wt: session.checkout.worktreePath || undefined })}>
                      <FileIcon path={touch.path} size={12} />
                      {touch.path}
                    </button>
                    <span className="xs muted truncate" title={touch.why}>
                      {touch.why}
                    </span>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </div>
    </details>
  );
}

/** The lines without the indentation they share, so a nested excerpt starts at the margin. */
export function dedent(text: string): string {
  const lines = text.split('\n');
  const shared = Math.min(...lines.filter(line => line.trim()).map(line => line.length - line.trimStart().length));
  return Number.isFinite(shared) && shared > 0 ? lines.map(line => line.slice(Math.min(shared, line.length - line.trimStart().length))).join('\n') : text;
}

/** The approach as its steps, one per line, without their numbers; a single line is one step. */
export function approachSteps(approach: string): string[] {
  return approach.split('\n').map(line => line.replace(/^\s*\d+[.)]\s*/, '').trim()).filter(Boolean);
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
  // One issue that holds is the plan itself: nothing more to say.
  if (verdicts.length === 1 && verdicts[0].verdict === 'holds' && !verdicts[0].note) return null;
  return (
    <div>
      {verdicts.length > 1 ? <div className="label-row">Issue verdicts</div> : null}
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
            {verdict.note ? (
              <p className="xs muted">
                <DocText text={verdict.note} shorten />
              </p>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Opens the note box that redirects the plan; the label says what a redirect means for this plan. */
export function RedirectPlanButton({ session, onClick, disabled }: { session: LoadedSession; onClick: () => void; disabled?: boolean }) {
  return (
    <Button variant="secondary" onClick={onClick} disabled={disabled}>
      {planFindsNothingToFix(session) ? 'I think it’s real…' : 'Redirect…'}
    </Button>
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
