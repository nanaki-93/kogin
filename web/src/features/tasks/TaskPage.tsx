// One task, laid out like a pull request: header with the next action, tabs
// for the conversation, files changed and checks, and a review-gate rail.

import { type ReactNode } from 'react';
import * as Tabs from '@radix-ui/react-tabs';
import {
  ArrowLeft, Archive, CheckCircle2, Circle, FileDiff, FlaskConical, GitBranch, GitMerge, MessageSquare, ShieldCheck, Square, ThumbsDown, Trash2,
} from 'lucide-react';
import { useSession, useTrust, useUsage } from '@/api/hooks';
import { costCaveat, costLabel, usageLabel } from '@/lib/usage';
import type { LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { usePageKeys } from '@/app/shortcuts';
import { useUI } from '@/app/store';
import { navigate, useRoute } from '@/lib/router';
import { openWorkspace } from '@/app/checkout';
import { canOverrideReview, checkSummary, effectiveEdits, investigating, nextUnreviewedPath, ownPlanFirst, planAwaitsDecision, planFindsNothingToFix, proposalReadiness, reviewProgress, sessionStatusEntry, workflowActive } from '@/lib/readiness';
import { activeOverride, agentRunLabel, agentsFinished, reviewVerdict } from '@/lib/workflow';
import { basename, plural, shortHash, timeAgo } from '@/lib/util';
import { Badge, Button, Callout, EmptyState, IconButton, Skeleton, cx } from '@/components/ui';
import { KindTag, TaskStatusBadge } from '@/components/domain';
import { FilesChanged } from '@/features/review/FilesChanged';
import { ChecksPanel } from '@/features/review/ChecksPanel';
import { ReanalyzeTask } from './ReanalyzeTask';
import { RunAgents } from './RunAgents';
import { CloseTask } from './CloseTask';
import { TaskOverview } from './TaskOverview';
import { AcceptTask } from './AcceptTask';
import { ApprovePlan, PlanDecisionProvider, RedirectPlanButton, usePlanDecision } from './PlanReview';
import { ApprovalNotes, OverrideReview } from './ReviewDecision';
import { TaskCodePane, TaskCodePreviewProvider } from './TaskCodePreview';

export function TaskPage({ id, tab }: { id: string; tab: string }) {
  const route = useRoute();
  const worktreePath = route.params.get('wt') ?? '';
  const { data: session, isLoading, error } = useSession(id, worktreePath, false);
  const current = tab === 'files' || tab === 'checks' ? tab : 'overview';

  usePageKeys(key => {
    if (!session) return false;
    if (key === '1' && current !== 'overview') navigate(`/tasks/${id}`);
    else if ((key === '2' || (key === 'r' && current !== 'files')) && current !== 'files') navigate(`/tasks/${id}/files`);
    else if (key === '3' && current !== 'checks') navigate(`/tasks/${id}/checks`);
    else return false;
    return true;
  });

  if (isLoading) return <div className="page-scroll"><div className="page-inner"><Skeleton lines={8} /></div></div>;
  if (error || !session) {
    return (
      <EmptyState icon={FileDiff} title="Task unavailable" action={<Button icon={ArrowLeft} onClick={() => navigate('/tasks')}>All tasks</Button>}>
        {error instanceof Error ? error.message : 'This task could not be loaded.'}
      </EmptyState>
    );
  }
  const edits = effectiveEdits(session);
  const checks = checkSummary(session.checks, session.checks_outdated);
  // A plan waiting for a decision is the page: no gate rail beside it.
  const deciding = planAwaitsDecision(session);

  return (
    <TaskCodePreviewProvider key={session.id}>
      <PlanDecisionProvider key={`${session.id}:${session.plan?.hash ?? ''}`} session={session}>
        <div className="task-page">
          <TaskHeader session={session} currentTab={current} />
          <Tabs.Root className="task-tabs" value={current} onValueChange={value => navigate(`/tasks/${id}${value === 'overview' ? '' : '/' + value}`, { file: route.params.get('file') ?? undefined })}>
            <Tabs.List className="tabs" aria-label="Task sections">
              <Tabs.Trigger value="overview" className="tab">
                <MessageSquare size={14} aria-hidden /> Conversation
              </Tabs.Trigger>
              <Tabs.Trigger value="files" className="tab">
                <FileDiff size={14} aria-hidden /> Files changed <span className="count">{edits.length}</span>
              </Tabs.Trigger>
              <Tabs.Trigger value="checks" className="tab">
                <FlaskConical size={14} aria-hidden /> Checks <span className={cx('count', `count-${checks.tone}`)}>{checks.total ? checks.label : '—'}</span>
              </Tabs.Trigger>
            </Tabs.List>
            <div className="task-body">
              <div className="task-main">
                <Tabs.Content value="overview" className="tab-panel">
                  <TaskOverview session={session} />
                </Tabs.Content>
                <Tabs.Content value="files" className="tab-panel tab-panel-fill">
                  <FilesChanged session={session} />
                </Tabs.Content>
                <Tabs.Content value="checks" className="tab-panel">
                  <ChecksPanel session={session} />
                </Tabs.Content>
              </div>
              <TaskCodePane session={session} />
              {current !== 'files' && !deciding ? <ReviewRail session={session} /> : null}
            </div>
          </Tabs.Root>
        </div>
      </PlanDecisionProvider>
    </TaskCodePreviewProvider>
  );
}

function TaskHeader({ session, currentTab }: { session: LoadedSession; currentTab: string }) {
  const planMode = useUI(s => s.planMode);
  const { redirecting, openRedirect } = usePlanDecision();
  const branch = session.git?.branch || (session.checkout.worktreePath ? basename(session.checkout.worktreePath) : '');
  return (
    <header className="task-header">
      <div className="grow">
        <h1 className="task-title">{session.title || session.id}</h1>
        <div className="row-wrap task-meta">
          <TaskStatusBadge entry={sessionStatusEntry(session)} />
          <KindTag kind={session.kind} />
          {branch ? (
            <span className="chip" title={session.checkout.worktreePath || 'main checkout'}>
              <GitBranch size={12} aria-hidden /> {branch}
            </span>
          ) : null}
          <span className="xs faint">v{session.revision}{session.hash ? ` · ${shortHash(session.hash)}` : ''}</span>
          {session.updated_at ? <span className="xs faint">updated {timeAgo(session.updated_at)}</span> : null}
        </div>
      </div>
      {planAwaitsDecision(session) && !ownPlanFirst(session, planMode) && !workflowActive(session) ? (
        <RedirectPlanButton
          session={session}
          onClick={() => {
            openRedirect();
            if (currentTab !== 'overview') navigate(`/tasks/${session.id}`);
          }}
          disabled={redirecting}
        />
      ) : null}
      <PrimaryAction session={session} />
      {offersRunBesidePrimary(session) ? <RunAgents session={session} /> : null}
      <DeleteTask session={session} />
    </header>
  );
}

/** Deletes the task (not while agents run) and returns to the task list. */
function DeleteTask({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  if (workflowActive(session)) return null;
  const stale = session.freshness !== 'current';
  const run = async () => {
    const task = { id: session.id, title: session.title, worktreePath: session.checkout.worktreePath, project_id: session.project_id, project_revision: session.project_revision, revision: session.revision, hash: session.hash };
    await actions.deleteTasks([task], () => openWorkspace('/tasks'));
  };
  return stale ? (
    <Button variant="secondary" icon={Trash2} onClick={() => void run()} disabled={Boolean(busy)}>Delete task</Button>
  ) : (
    <IconButton icon={Trash2} label="Delete task" onClick={() => void run()} disabled={Boolean(busy)} />
  );
}

/**
 * A run of the agents is on offer whenever none is active and the task is
 * neither applied nor stale: as the primary action before the first run and
 * while the agents have to run again, otherwise beside the review step. A
 * stale task is re-analyzed first.
 */
function offersRunBesidePrimary(session: LoadedSession): boolean {
  const step = proposalReadiness(session).step;
  return !workflowActive(session) && !['applied', 'plan', 'generate', 'agents', 'stale', 'checkout'].includes(step);
}

/** The single most useful next step, as a button, on every tab. */
function PrimaryAction({ session }: { session: LoadedSession }) {
  const route = useRoute();
  const actions = useActions();
  const busy = useBusy();
  const trust = useTrust().data;
  const ready = proposalReadiness(session);
  const reviewMode = useUI(state => state.reviewMode);
  const planMode = useUI(state => state.planMode);
  if (workflowActive(session)) {
    return (
      <Button variant="secondary" icon={Square} onClick={() => void actions.cancelAgents(session)} disabled={session.workflow?.status === 'canceling' || Boolean(busy)}>
        Stop agents
      </Button>
    );
  }
  switch (ready.step) {
    case 'plan':
      // Your own plan comes first; the decisions appear with the agent's plan.
      if (ownPlanFirst(session, planMode)) return null;
      return planFindsNothingToFix(session) ? <CloseTask session={session} variant="primary" size="md" /> : <ApprovePlan session={session} />;
    case 'generate':
    case 'agents':
      return <RunAgents session={session} variant="primary" />;
    case 'stale':
      return <ReanalyzeTask session={session} />;
    case 'review': {
      // On the files, the next step is the next file nobody viewed yet.
      const onFiles = route.path.endsWith('/files');
      // Hunk by hunk, the whole change is on the page and its bar ends the review.
      if (onFiles && reviewMode === 'hunks') return null;
      const next = onFiles ? nextUnreviewedPath(session, session.changes ?? [], route.params.get('file') ?? '') : '';
      return (
        <Button variant="primary" icon={FileDiff} onClick={() => navigate(`/tasks/${session.id}/files`, next ? { ...Object.fromEntries(route.params.entries()), file: next, hunk: undefined, line: undefined } : {})}>
          {next ? 'Next unviewed file' : 'Review files'}
        </Button>
      );
    }
    case 'checks':
      return (
        <Button variant="primary" icon={FlaskConical} onClick={() => void actions.runChecks(session)} disabled={!trust?.trusted || Boolean(busy)}>
          Run checks
        </Button>
      );
    case 'accept':
      return <AcceptTask session={session} />;
    case 'applied':
      return session.worktree ? <MergeFromBranches primary /> : null;
    default:
      return null;
  }
}

function ReviewRail({ session }: { session: LoadedSession }) {
  const ready = proposalReadiness(session);
  const progress = reviewProgress(session);
  const checks = checkSummary(session.checks, session.checks_outdated);
  const workflow = session.workflow;
  const verdict = reviewVerdict(workflow, session.hash);
  const overridden = Boolean(activeOverride(workflow, session.hash));
  const reviewers = overridden ? 'Overridden by you' : verdict === 'approve' ? 'Approved' : verdict === 'changes_requested' ? 'Changes requested' : verdict === 'pending' ? 'Pending' : workflow ? 'Run after the agents' : (session.changes ?? []).length ? 'Not part of a manual proposal' : 'Run after the agents';
  // A plan the investigation proposed comes first; the agents run from it.
  const plan = session.plan?.source === 'investigator' ? session.plan : undefined;
  const planDetail = planAwaitsDecision(session) ? 'Needs your approval' : plan?.approved_hash && plan.approved_hash === plan.hash ? 'Approved' : investigating(session) ? 'Investigating…' : plan ? 'Superseded' : 'Investigating…';
  const agentsDetail = planAwaitsDecision(session) ? 'After you approve the plan' : agentRunLabel(workflow, Boolean((session.changes ?? []).length));
  const steps: { label: string; done: boolean; active: boolean; detail: ReactNode; onClick?: () => void }[] = [
    ...(plan || investigating(session) ? [{ label: 'Plan', done: Boolean(plan?.approved_hash) && plan?.approved_hash === plan?.hash, active: ready.step === 'plan' || investigating(session), detail: planDetail }] : []),
    { label: 'Agents finished', done: agentsFinished(session), active: ready.step === 'agents' || ready.step === 'generate', detail: agentsDetail },
    { label: 'Reviewers', done: verdict === 'approve' || overridden, active: false, detail: reviewers },
    { label: 'Files viewed', done: ready.filesReviewed, active: ready.step === 'review', detail: `${progress.reviewed} of ${progress.total}`, onClick: () => navigate(`/tasks/${session.id}/files`) },
    { label: 'Checks', done: ready.checksPassed, active: ready.step === 'checks', detail: checks.label, onClick: () => navigate(`/tasks/${session.id}/checks`) },
  ];
  return (
    <aside className="rail" aria-label="Review gate">
      <section className="rail-section">
        <div className="rail-title">Ready to accept?</div>
        <ol className="gate">
          {steps.map(step => (
            <li key={step.label} className={cx('gate-step', step.done && 'is-done', step.active && 'is-active')}>
              {step.done ? <CheckCircle2 size={15} className="tone-success" aria-hidden /> : <Circle size={15} className={step.active ? 'tone-accent' : 'faint'} aria-hidden />}
              <button type="button" className="gate-label" onClick={step.onClick} disabled={!step.onClick}>
                <span>{step.label}</span>
                <span className="xs muted">{step.detail}</span>
              </button>
            </li>
          ))}
        </ol>
        <p className="xs muted">{ready.message}</p>
        <ApprovalNotes session={session} />
      </section>
      {canOverrideReview(session) ? <OverrideReview session={session} /> : null}
      {(session.changes ?? []).length || session.state === 'applied' || ready.step === 'checkout' ? <ApplyBox session={session} /> : null}
      <section className="rail-section">
        <div className="rail-title">Details</div>
        <dl className="details">
          <dt>Checkout</dt>
          <dd className="truncate" title={session.checkout.worktreePath || 'main checkout'}>{session.checkout.worktreePath ? basename(session.checkout.worktreePath) : 'main checkout'}</dd>
          {session.git?.branch ? (<><dt>Branch</dt><dd>{session.git.branch}</dd></>) : null}
          <dt>Scope</dt>
          <dd>{plural((session.targets ?? []).length, 'file')}</dd>
          {session.finding_targets?.length ? (<><dt>Fixes</dt><dd>{plural(session.finding_targets.length, 'issue')}</dd></>) : null}
          {session.commit ? (<><dt>Commit</dt><dd className="mono">{session.commit.state === 'committed' ? shortHash(session.commit.oid) : session.commit.state}</dd></>) : null}
          {workflow?.stages?.find(s => s.model)?.model ? (<><dt>Models</dt><dd className="truncate">{[...new Set(workflow.stages.map(s => s.model?.model).filter(Boolean))].join(', ')}</dd></>) : null}
          <TaskSpend id={session.id} />
        </dl>
      </section>
    </aside>
  );
}

/** What agents spent on this task across its runs and revisions. */
function TaskSpend({ id }: { id: string }) {
  const row = useUsage().data?.tasks.find(task => task.session_id === id);
  if (!row?.usage.model_calls) return null;
  const cost = costLabel(row.cost);
  return (
    <>
      <dt>Spent</dt>
      <dd title={costCaveat(row.cost) || undefined}>
        <a href="#/settings/usage" onClick={e => { e.preventDefault(); navigate('/settings/usage'); }}>
          {usageLabel(row.usage)}{cost ? ` · ${cost}` : ''}
        </a>
      </dd>
    </>
  );
}

/** After Accept, the branch is merged from the Branches page: the header's next step, a link beside it in the rail. */
function MergeFromBranches({ primary }: { primary?: boolean }) {
  return (
    <Button variant={primary ? 'primary' : 'secondary'} icon={GitMerge} onClick={() => navigate('/git')}>
      Merge from Branches
    </Button>
  );
}

function ApplyBox({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const ready = proposalReadiness(session);
  const input = session.workflow?.input;

  if (session.state === 'applied') {
    return (
      <section className="rail-section">
        <Callout tone="success" icon={ShieldCheck} title={session.worktree ? 'Accepted' : 'Applied'}>
          {ready.message}
        </Callout>
        {session.worktree ? <MergeFromBranches /> : null}
      </section>
    );
  }
  if (ready.step === 'checkout') {
    return (
      <section className="rail-section">
        <Callout tone="neutral" icon={Archive} title="Read only">
          Kogin no longer changes tasks made in the main checkout. Delete it, and create it again if it is still needed: it gets its own worktree.
        </Callout>
      </section>
    );
  }
  if (!ready.editable) return null;
  return (
    <section className="rail-section">
      {/* The header carries the next step; the rail repeats Accept only once it can run. */}
      {ready.canAccept ? <AcceptTask session={session} /> : null}
      {input?.status === 'waiting' ? (
        <Button variant="ghost" icon={ThumbsDown} onClick={() => void actions.reject(session)} disabled={Boolean(busy)}>
          Reject proposal
        </Button>
      ) : null}
      {workflowActive(session) ? <Badge tone="info">Agents are still working</Badge> : null}
    </section>
  );
}
