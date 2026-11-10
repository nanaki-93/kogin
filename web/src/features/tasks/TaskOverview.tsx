// The task's conversation: what was asked, what the agents did stage by
// stage, what reviewers said, and a box to ask for a revision.

import { useEffect, useState } from 'react';
import { Bot, CircleCheck, CornerDownLeft, FileText, Gavel, Hourglass, ListChecks, MessageSquare, Plus, ShieldAlert, Square, User } from 'lucide-react';
import type { ChangeWorkflow, LoadedSession } from '@/api/types';
import { useActions, useBusy, usePendingRevision } from '@/app/actions';
import { behaviorChanges, openComments, ownPlanFirst, planAwaitsDecision, proposalReadiness, workflowActive } from '@/lib/readiness';
import { activeOverride, issueStepState, pauseTitle, stageStatusLabel, workflowSpecialists, workflowStages, type SpecialistView, type StageView } from '@/lib/workflow';
import { formatDuration, formatElapsed, plural, timeAgo } from '@/lib/util';
import { costCaveat, costLabel, formatTokens, usageLabel } from '@/lib/usage';
import { firstSentence } from '@/lib/docs';
import { Badge, Button, Callout, PhaseIcon, Textarea, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';
import { navigate } from '@/lib/router';
import { openNewTask } from '@/app/store';
import { CloseTask } from './CloseTask';
import { CodeRules } from './CodeRules';
import { PlanReview } from './PlanReview';
import { ReanalyzeTask } from './ReanalyzeTask';
import { MainChanged } from './RetakeTask';
import { RunAgents } from './RunAgents';
import { ReviewItems } from './ReviewItems';
import { LiveRun } from './LiveRun';
import { useOpenTaskFile } from './TaskCodePreview';

export function TaskOverview({ session, redirecting, onRedirectDone }: { session: LoadedSession; redirecting: boolean; onRedirectDone: () => void }) {
  // A plan that waits for your decision is the whole page: the run's
  // stages, the rules and the conversation come back once it is decided.
  if (planAwaitsDecision(session)) {
    return (
      <div className="page-inner stack-lg">
        <MainChanged session={session} />
        <PlanReview session={session} redirecting={redirecting} onRedirectDone={onRedirectDone} />
        <Brief session={session} open={ownPlanFirst(session)} />
        {ownPlanFirst(session) ? null : <Investigated session={session} />}
      </div>
    );
  }
  return (
    <div className="page-inner stack-lg">
      <MainChanged session={session} />
      {workflowActive(session) && session.workflow ? <LiveRun session={session} /> : null}
      <PlanReview session={session} redirecting={false} onRedirectDone={onRedirectDone} />
      <Brief session={session} />
      <CodeRules session={session} />
      {session.workflow ? <AgentRun session={session} /> : null}
      <Conversation session={session} />
      <Revise session={session} />
    </div>
  );
}

/** Who investigated, with what and for how long: one line under the plan it proposed. */
function Investigated({ session }: { session: LoadedSession }) {
  const stage = session.workflow ? workflowStages(session.workflow).find(entry => entry.name === 'plan') : undefined;
  if (!stage) return null;
  const lead = `${stage.model ? `Investigated by ${stage.model}` : 'Investigated'}${stage.durationMs ? ` in ${formatDuration(stage.durationMs)}` : ''}`;
  const tools = (stage.tools ?? '').replace(/^Worked with tools:\s*/, '');
  return <p className="xs faint plan-investigated">{[lead, tools].filter(Boolean).join(' · ')}</p>;
}

function Brief({ session, open }: { session: LoadedSession; /** Open while you write your own plan from it. */ open?: boolean }) {
  const openTaskFile = useOpenTaskFile(session.checkout.worktreePath);
  // A plan the investigation proposed says what the task is; the brief it
  // was investigated from folds under it.
  const investigated = session.plan?.source === 'investigator';
  const plan = investigated ? undefined : session.plan;
  const criteria = session.acceptance_criteria ?? [];
  const targets = session.targets ?? [];
  const body = (
    <div className="card-body stack">
      {plan?.approach ? <p className="prose">{plan.approach}</p> : investigated ? null : <p className="muted small">No description. The agents work from the title, scope and criteria.</p>}
      {plan?.assumptions?.length ? (
        <div className="small">
          <span className="muted">Assumptions: </span>
          {plan.assumptions.join(' · ')}
        </div>
      ) : null}
      {criteria.length ? (
        <div>
          <div className="label-row">
            <ListChecks size={13} aria-hidden /> Done when
          </div>
          <ol className="criteria-read">
            {criteria.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ol>
        </div>
      ) : null}
      <div>
        <div className="label-row">Scope · files agents may change</div>
        <div className="chips">
          {targets.map(t => (
            <button key={t.path} type="button" className="chip chip-file" onClick={() => openTaskFile(t.path)} title={`Inspect ${t.path}`}>
              <FileIcon path={t.path} size={12} />
              {t.path}
              {!t.exists ? <span className="faint">new</span> : null}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
  if (investigated) {
    return (
      <details className="card card-fold" aria-label="Task brief" open={open || undefined}>
        <summary className="card-head">
          <FileText size={15} aria-hidden />
          <span className="strong">Brief</span>
          <span className="xs muted">{[criteria.length ? `done when ${plural(criteria.length, 'criterion', 'criteria')}` : '', plural(targets.length, 'file')].filter(Boolean).join(' · ')}</span>
        </summary>
        {body}
      </details>
    );
  }
  return (
    <section className="card" aria-label="Task brief">
      <div className="card-head">
        <FileText size={15} aria-hidden />
        <span className="strong">Brief</span>
        <span className="spacer" />
        <span className="xs faint">updated {timeAgo(session.updated_at)}</span>
      </div>
      {body}
    </section>
  );
}

function AgentRun({ session }: { session: LoadedSession }) {
  const workflow = session.workflow!;
  const stages = workflowStages(workflow);
  const specialists = workflowSpecialists(workflow);
  const active = workflowActive(session);
  // A run the main branch's change stopped is told about above, with its retake.
  const stoppedByMain = workflow.status === 'stale' && Boolean(session.main_changed);
  const ended = ['failed', 'canceled', 'stale', 'outdated', 'interrupted', 'paused', 'nothing_to_fix', 'waiting'].includes(workflow.status) && !stoppedByMain;
  const override = activeOverride(workflow, session.hash);
  return (
    <section className="card" aria-label="Agent run">
      <div className="card-head">
        <Bot size={15} aria-hidden />
        <span className="strong">Agent run</span>
        {workflow.attempt && workflow.attempt > 1 ? <Badge>attempt {workflow.attempt}</Badge> : null}
        {workflow.auto_repair ? <Badge tone="neutral">auto-repair {workflow.auto_repair_attempts ?? 0}/2</Badge> : null}
        {active && workflow.status === 'canceling' ? <><span className="spacer" /><span className="xs muted">Stopping…</span></> : null}
      </div>
      {ended && workflow.reason ? (
        <div className="card-body">
          <Callout {...endedCallout(workflow)} icon={workflow.status === 'paused' || workflow.status === 'waiting' ? Hourglass : workflow.status === 'nothing_to_fix' ? CircleCheck : ShieldAlert} action={endedAction(session)}>
            {workflow.reason}
          </Callout>
        </div>
      ) : null}
      {override ? (
        <div className="card-body">
          <Callout tone="info" icon={Gavel} title={`You overrode the reviewers ${timeAgo(override.at)}`}>
            {override.reason}
          </Callout>
        </div>
      ) : null}
      <ol className="timeline">
        {stages.map(stage => (
          <Stage key={stage.name} stage={stage} specialists={stage.name === 'review' ? specialists : []} session={session} />
        ))}
      </ol>
    </section>
  );
}

/**
 * What the testing agent decided beyond its tests: disputed tests it ruled
 * on, and gaps the change cannot cover, each a task you can start.
 */
function TestDecisions({ session }: { session: LoadedSession }) {
  const followUps = session.follow_ups ?? [];
  const decided = session.decided_tests ?? [];
  if (!followUps.length && !decided.length) return null;
  return (
    <ul className="issue-steps" aria-label="Testing decisions">
      {decided.length ? (
        <li className="issue-step scope-grant">
          <span className="xs muted">The coding agent disputed {decided.length === 1 ? 'a test' : `${decided.length} tests`}; the testing agent decided: </span>
          <span className="small mono">{decided.join(', ')}</span>
        </li>
      ) : null}
      {followUps.map(followUp => (
        <li key={followUp.title} className="issue-step scope-grant">
          <div className="issue-step-head">
            <span className="xs muted">Follow-up</span>
            <span className="small truncate" title={followUp.title}>
              {followUp.title}
            </span>
            <span className="spacer" />
            <Button size="sm" variant="ghost" icon={Plus} onClick={() => openNewTask({ title: followUp.title, description: followUp.detail, paths: followUp.path ? [followUp.path] : [] })}>
              New task
            </Button>
          </div>
          <p className="xs muted">{followUp.detail}</p>
        </li>
      ))}
    </ul>
  );
}

/** What an ended run offers: close a task with nothing to fix, open the task one waits for, re-analyze a stale one, or run the agents again. */
function endedAction(session: LoadedSession) {
  const workflow = session.workflow!;
  if (workflow.status === 'nothing_to_fix') return <CloseTask session={session} />;
  if (workflow.status === 'waiting') {
    const wait = workflow.waiting;
    return wait ? (
      <Button size="sm" variant="secondary" onClick={() => navigate(`/tasks/${wait.task_id}`, { wt: wait.worktree_path })}>
        Open that task
      </Button>
    ) : null;
  }
  if (session.freshness !== 'current') return <ReanalyzeTask session={session} variant="secondary" size="sm" />;
  return <RunAgents session={session} size="sm" />;
}

/** How an ended run reads: a revision after it is not a failure, nor a pause at a plan's limit. */
function endedCallout(workflow: ChangeWorkflow): { tone: 'neutral' | 'info' | 'danger'; title: string } {
  const status = workflow.status;
  if (status === 'paused') return { tone: 'info', title: pauseTitle(workflow.pause) };
  if (status === 'nothing_to_fix') return { tone: 'neutral', title: 'Nothing to fix' };
  if (status === 'waiting') return { tone: 'info', title: workflow.waiting ? `Waiting for “${workflow.waiting.title}”` : 'Waiting for another task' };
  if (status === 'canceled') return { tone: 'neutral', title: 'Stopped' };
  if (status === 'stale') return { tone: 'info', title: 'Its files changed during this run' };
  if (status === 'outdated') return { tone: 'info', title: 'The proposal changed after this run' };
  return { tone: 'danger', title: 'The run did not finish' };
}

function Stage({ stage, specialists, session }: { stage: StageView; specialists: SpecialistView[]; session: LoadedSession }) {
  return (
    <li className={cx('tl-item', `tl-${stage.tone}`)}>
      <div className="tl-marker">
        <PhaseIcon tone={stage.tone} size={16} label={stage.status} />
      </div>
      <div className="tl-content">
        <div className="row">
          <span className="strong">{stage.label}</span>
          <span className="xs muted">{stage.actor}</span>
          <span className="spacer" />
          {stage.model ? <span className="xs faint mono">{stage.model}</span> : null}
          {stage.usage?.model_calls ? (
            <span className="xs faint nowrap" title={`${usageLabel(stage.usage)}${costCaveat(stage.cost) ? ` — ${costCaveat(stage.cost)}` : ''}`}>
              {[`${formatTokens(stage.usage.input_tokens + stage.usage.output_tokens)} tokens`, costLabel(stage.cost)].filter(Boolean).join(' · ')}
            </span>
          ) : null}
          <span className="xs faint nowrap">{stage.durationMs ? formatDuration(stage.durationMs) : stageStatusLabel(stage)}</span>
        </div>
        {stage.tools ? <p className="xs muted">{stage.tools}</p> : null}
        {stage.reason ? <p className="small muted">{stage.reason}</p> : null}
        {stage.name === 'create' ? <IssueSteps session={session} /> : null}
        {stage.name === 'create' ? <ScopeGrants session={session} /> : null}
        {stage.name === 'test' ? <TestDecisions session={session} /> : null}
        {specialists.length ? (
          <div className="reviewers">
            <BehaviorChanges session={session} />
            {specialists.map(sp => (
              <Reviewer key={sp.id} reviewer={sp} session={session} />
            ))}
          </div>
        ) : null}
      </div>
    </li>
  );
}

/** A task that fixes its issues one at a time: each issue's step, its commit or why it was not fixed. */
function IssueSteps({ session }: { session: LoadedSession }) {
  const issues = session.issues ?? [];
  if (issues.length < 2) return null;
  return (
    <ol className="issue-steps" aria-label="Issues, one at a time">
      {issues.map((issue, index) => {
        const state = issueStepState(issue.status);
        return (
          <li key={issue.finding_id} className="issue-step">
            <div className="issue-step-head">
              <PhaseIcon tone={state.tone} size={14} label={state.label} />
              <span className="small truncate" title={issue.title}>
                {index + 1}. {issue.title}
              </span>
              <span className="spacer" />
              {issue.commit ? (
                <span className="xs faint mono" title={`Commit ${issue.commit}`}>
                  {issue.commit.slice(0, 7)}
                </span>
              ) : null}
              <Badge tone={state.badge}>{state.label}</Badge>
            </div>
            {issue.status === 'not_fixed' && issue.reason ? <p className="xs muted">{issue.reason} It is open again in Issues.</p> : null}
            {issue.status === 'does_not_hold' && issue.reason ? <p className="xs muted">{issue.reason} Merging the task dismisses it; you can reopen it in Issues.</p> : null}
            {issue.status === 'fixed' && issue.reason ? <p className="xs muted">{issue.reason}</p> : null}
          </li>
        );
      })}
    </ol>
  );
}

/** Files outside the fix's folders its agents were allowed to change, and why. */
function ScopeGrants({ session }: { session: LoadedSession }) {
  const grants = session.scope_grants ?? [];
  if (!grants.length) return null;
  return (
    <ul className="issue-steps" aria-label="Added to the fix">
      {grants.map(grant => (
        <li key={grant.path} className="issue-step scope-grant">
          <div className="issue-step-head">
            <span className="xs muted">Added to the fix</span>
            <span className="small mono truncate" title={grant.path}>
              {grant.path}
            </span>
          </div>
          <p className="xs muted">{grant.reason}</p>
        </li>
      ))}
    </ul>
  );
}

/** What a user, a script or a caller sees differently after this version; Accept asks you to accept them. */
function BehaviorChanges({ session }: { session: LoadedSession }) {
  const changes = behaviorChanges(session);
  if (!changes.length) return null;
  const accepted = session.reviewed_hash === session.hash;
  return (
    <div className="behavior-changes" role="group" aria-label="Behavior changes">
      <div className="row">
        <span className="strong small">{plural(changes.length, 'behavior change')}</span>
        <Badge tone={accepted ? 'success' : 'warning'}>{accepted ? 'Accepted' : 'To accept'}</Badge>
      </div>
      <ReviewItems items={changes} session={session} advisory />
    </div>
  );
}

function Reviewer({ reviewer, session }: { reviewer: SpecialistView; session: LoadedSession }) {
  const [open, setOpen] = useState(reviewer.verdict === 'changes_requested');
  const verdict = reviewer.verdict === 'approve' ? { label: 'Approved', tone: 'success' as const } : reviewer.verdict === 'changes_requested' ? { label: 'Changes requested', tone: 'warning' as const } : { label: reviewer.status, tone: 'neutral' as const };
  return (
    <div className={cx('reviewer', open && 'is-open')}>
      <button type="button" className="reviewer-head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <PhaseIcon tone={reviewer.tone} size={14} />
        <span className="strong small">{reviewer.label}</span>
        <Badge tone={verdict.tone}>{verdict.label}</Badge>
        {reviewer.findings.length ? <span className="xs muted">{plural(reviewer.findings.length, 'blocking finding')}</span> : null}
        {reviewer.notes.length ? <span className="xs faint">{plural(reviewer.notes.length, 'note')}</span> : null}
        <span className="spacer" />
        <span className="xs faint">{reviewer.evidence === 'unmeasured' ? 'unmeasured advice' : 'model advice'}</span>
      </button>
      {open ? (
        <div className="reviewer-body small">
          {reviewer.summary ? <p title={reviewer.summary}>{firstSentence(reviewer.summary, 200)}</p> : null}
          {reviewer.reason ? <p className="muted">{reviewer.reason}</p> : null}
          {reviewer.findings.length ? <ReviewItems items={reviewer.findings} session={session} /> : null}
          {reviewer.notes.length ? (
            <>
              <div className="label-row">Advisory</div>
              <ReviewItems items={reviewer.notes} session={session} advisory />
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function Conversation({ session }: { session: LoadedSession }) {
  const messages = (session.messages ?? []).filter(m => m.content?.trim());
  if (!messages.length) return null;
  const hasUserMessages = messages.some(m => m.role === 'user');
  const quiet = Boolean(session.workflow) && !hasUserMessages;
  const list = (
    <ol className="messages">
      {messages.map((m, i) => (
        <li key={i} className={cx('message', m.role === 'user' ? 'from-user' : 'from-agent')}>
          <span className="message-avatar" aria-hidden>
            {m.role === 'user' ? <User size={13} /> : <Bot size={13} />}
          </span>
          <div className="message-body">
            <div className="xs muted">{m.role === 'user' ? 'You' : 'Agent'}</div>
            <AgentMessage content={m.content} />
          </div>
        </li>
      ))}
    </ol>
  );
  if (quiet) {
    return (
      <section className="card card-quiet" aria-label="Conversation">
        <details className="card-fold">
          <summary className="card-head">
            <MessageSquare size={15} aria-hidden />
            <span className="strong">Agent notes</span>
            <span className="xs faint">{plural(messages.length, 'note')}</span>
          </summary>
          {list}
        </details>
      </section>
    );
  }
  return (
    <section className="card" aria-label="Conversation">
      <div className="card-head">
        <MessageSquare size={15} aria-hidden />
        <span className="strong">Conversation</span>
        <span className="xs faint">{messages.length}</span>
      </div>
      {list}
    </section>
  );
}

/** An agent's report with its tool counts folded into a faint line, so the report is what reads. */
function AgentMessage({ content }: { content: string }) {
  const marker = content.indexOf('\n\nTools:');
  if (marker < 0) return <div className="prose small">{content}</div>;
  return (
    <>
      <div className="prose small">{content.slice(0, marker).trim()}</div>
      <div className="xs faint" title={content.slice(marker + 2).trim()}>{content.slice(marker + 2).trim()}</div>
    </>
  );
}

function Revise({ session }: { session: LoadedSession }) {
  const [text, setText] = useState('');
  const [withComments, setWithComments] = useState(true);
  const actions = useActions();
  const busy = useBusy();
  const pending = usePendingRevision();
  const ready = proposalReadiness(session);
  const comments = openComments(session).length;
  // While a plan waits for a decision, nothing is generated: the plan card decides.
  if (!ready.editable || workflowActive(session) || ready.step === 'plan') return null;
  const revising = pending?.sessionId === session.id ? pending : null;
  const sendComments = comments > 0 && withComments;
  const submit = async () => {
    if (await actions.refine(session, text, sendComments)) setText('');
  };
  return (
    <section className="revise" aria-label="Ask for a revision">
      <Textarea
        rows={3}
        placeholder={(session.changes ?? []).length ? 'Ask the agent to change something in this proposal…' : 'Describe the change to generate it without the full agent run…'}
        value={text}
        onChange={e => setText(e.target.value)}
        onKeyDown={e => {
          if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
            e.preventDefault();
            void submit();
          }
        }}
        aria-label="Revision request"
      />
      {comments ? (
        <label className="check-label small">
          <input type="checkbox" checked={withComments} onChange={e => setWithComments(e.target.checked)} />
          Include my {plural(comments, 'line comment')}
        </label>
      ) : null}
      <div className="row">
        {revising ? <RevisionProgress since={revising.since} onStop={() => void actions.stopRevision(session)} /> : <span className="xs faint">A revision creates a new version; earlier review marks and checks no longer apply.</span>}
        <span className="spacer" />
        <Button variant="secondary" icon={CornerDownLeft} onClick={() => void submit()} disabled={(!text.trim() && !sendComments) || Boolean(busy)} loading={busy === 'Generating…'} kbd="mod+Enter">
          {(session.changes ?? []).length ? 'Revise' : 'Generate'}
        </Button>
      </div>
    </section>
  );
}

/** How long the agent has worked on a revision, with Stop: the request can take minutes. */
function RevisionProgress({ since, onStop }: { since: number; onStop: () => void }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return (
    <span className="revise-progress" role="status">
      <span className="small">
        Agent working ·{' '}
        <span className="mono" aria-live="off">
          {formatElapsed(now - since)}
        </span>
      </span>
      <span className="xs faint">This can take a few minutes.</span>
      <Button size="sm" variant="ghost" icon={Square} onClick={onStop}>
        Stop
      </Button>
    </span>
  );
}
