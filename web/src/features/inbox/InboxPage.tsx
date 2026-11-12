// The landing page: what needs you now, what agents are doing, and the state
// of the project, so a glance answers "what should I do next?".

import { useMemo } from 'react';
import { Bot, CircleAlert, FolderOpen, GitMerge, GraduationCap, Inbox, Plus, ShieldOff, Sparkles, Wrench } from 'lucide-react';
import { useFindings, useGitWorkspace, useLessons, useMainProject, useOverview, useSetup, useTasks, useTrust } from '@/api/hooks';
import { openNewTask } from '@/app/store';
import { computeAtlasMastery } from '@/lib/concepts';
import { navigate } from '@/lib/router';
import { ownFirst, taskStatus } from '@/lib/readiness';
import { plural, severityRank, timeAgo } from '@/lib/util';
import { runActive, runProgress, statusLabel, statusTone } from '@/lib/analysis';
import { Badge, Button, Callout, EmptyState, Kbd, ListGroup, Progress, Skeleton } from '@/components/ui';
import { SeverityBadge, isCountedOpenFinding, isHousekeepingFinding, isWorkFinding } from '@/components/domain';
import { QuarantineNotice } from '@/features/settings/Quarantine';
import { AgentSettingsNotice } from '@/features/settings/AgentSettingsNotice';
import { TaskRow } from '@/features/tasks/TaskListPage';
import { OpenProject } from '@/features/settings/OpenProject';
import { isHousekeepingTask } from '@/lib/housekeeping';
import { AskNext } from './AskNext';
import { Housekeeping } from './Housekeeping';
import { NextUp } from './NextUp';

export function InboxPage() {
  const main = useMainProject();
  const { tasks, isLoading } = useTasks();
  const counted = (useFindings().data?.findings ?? []).filter(isCountedOpenFinding);
  // Issues in the code that runs are the work; issues in tests are housekeeping.
  const findings = counted.filter(isWorkFinding);
  const upkeep = counted.filter(isHousekeepingFinding).length;
  const overview = useOverview().data;
  const git = useGitWorkspace().data;
  const trust = useTrust().data;

  if (main.isError) {
    return (
      <div className="page-scroll">
        <div className="page-inner page-narrow">
          <EmptyState icon={FolderOpen} title="Open a project to start">
            Kogin works on one local repository at a time. Choose its folder to index it.
          </EmptyState>
          <OpenProject />
        </div>
      </div>
    );
  }

  // What you asked for comes before what analysis found, in every group.
  const waiting = tasks.filter(t => ['needs_you', 'ready'].includes(taskStatus(t).status));
  // Reviewed upkeep of the tests gets its own group, accepted together; the rest needs you one by one.
  const housekeeping = waiting.filter(t => isHousekeepingTask(t) && t.workflow_status === 'awaiting_human_review');
  const needs = ownFirst(waiting.filter(t => !housekeeping.includes(t)));
  const failed = ownFirst(tasks.filter(t => ['failed', 'stale'].includes(taskStatus(t).status)));
  const running = ownFirst(tasks.filter(t => taskStatus(t).status === 'running'));
  const recent = tasks.filter(t => taskStatus(t).status === 'applied').slice(0, 4);
  const toMerge = (git?.worktrees ?? []).filter(tree => !tree.current && tree.mergeable);
  const topIssues = [...findings].sort((a, b) => severityRank(a.severity) - severityRank(b.severity)).slice(0, 5);
  const coverage = overview?.analysis_coverage;
  const run = overview?.analysis_run;
  const progress = runProgress(run);
  const nothing = !waiting.length && !failed.length && !running.length && !toMerge.length;

  return (
    <div className="page-scroll">
      <div className="page-inner">
        <div className="page-head">
          <div className="grow">
            <h1 className="page-title">Inbox</h1>
            <p className="page-sub">{isLoading ? ' ' : inboxSummary(waiting.length, running.length, failed.length, toMerge.length)}</p>
          </div>
        </div>
        {trust && !trust.trusted ? (
          <Callout tone="warning" icon={ShieldOff} title="Agents can't verify their work yet" action={<Button size="sm" onClick={() => navigate('/settings/trust')}>Allow checks</Button>}>
            Tests and lint run only after you allow this project to run its check commands in an isolated copy.
          </Callout>
        ) : null}
        <SetupNotice />
        <AgentSettingsNotice />
        <QuarantineNotice />
        <AskNext project={main.data} />
        {isLoading ? (
          <Skeleton lines={6} />
        ) : nothing && !tasks.length ? (
          <div className="welcome">
            <div className="welcome-card">
              <Sparkles size={22} className="tone-accent" aria-hidden />
              <h2>Describe a change. Agents do the rest; you stay in control.</h2>
              <p className="muted">An agent first reads the code and proposes a plan you approve. Agents then write the change and its tests in an isolated copy, run the checks and review it; you read the result against the plan and accept the exact version you reviewed as a commit on the task's own branch.</p>
              <div className="row">
                <Button icon={Plus} onClick={() => openNewTask()}>
                  New task
                </Button>
                <span className="xs muted">
                  or press <Kbd>C</Kbd> anywhere
                </span>
              </div>
            </div>
          </div>
        ) : nothing ? (
          <EmptyState icon={Inbox} title="Nothing needs you">
            No task is waiting for review and no agent is running.
          </EmptyState>
        ) : null}

        {!isLoading && (!nothing || topIssues.length) ? <NextUp needs={needs} failed={failed} toMerge={toMerge} issues={topIssues} project={main.data} /> : null}
        <div className="inbox-grid">
          <div className="stack-lg">
            <Housekeeping tasks={housekeeping} />
            {needs.length ? (
              <ListGroup title="Needs you" count={needs.length}>
                {needs.map(t => <TaskRow key={t.id} task={t} />)}
              </ListGroup>
            ) : null}
            {running.length ? (
              <ListGroup title="Agents working" count={running.length} icon={Bot}>
                {running.map(t => <TaskRow key={t.id} task={t} />)}
              </ListGroup>
            ) : null}
            {failed.length ? (
              <ListGroup title="Failed or stale" count={failed.length}>
                {failed.map(t => <TaskRow key={t.id} task={t} />)}
              </ListGroup>
            ) : null}
            {toMerge.length ? (
              <ListGroup title="Ready to merge" count={toMerge.length} icon={GitMerge}>
                {toMerge.map(tree => (
                  <button key={tree.path} type="button" className="list-row" onClick={() => navigate('/git', { path: tree.path })}>
                    <GitMerge size={14} className="tone-warning" aria-hidden />
                    <span className="list-title truncate">{tree.branch || tree.path}</span>
                    <span className="spacer" />
                    <span className="xs muted">{plural((tree.sessions ?? []).length + (tree.completed ?? []).length, 'task')}</span>
                    {(tree.overlap_paths ?? []).length ? <span className="badge badge-warning">{plural(tree.overlap_paths!.length, 'overlap')}</span> : null}
                  </button>
                ))}
              </ListGroup>
            ) : null}
            {recent.length ? (
              <ListGroup title="Recently applied" count={recent.length}>
                {recent.map(t => <TaskRow key={t.id} task={t} compact />)}
              </ListGroup>
            ) : null}
          </div>
          <div className="stack-lg">
            <div className="card">
              <div className="card-head">
                <CircleAlert size={15} aria-hidden />
                <span className="strong">Found by analysis</span>
                {findings.length ? <span className="count">{findings.length}</span> : null}
                <span className="spacer" />
                <button type="button" className="link-btn small" onClick={() => navigate('/issues')}>
                  View all
                </button>
              </div>
              {topIssues.length ? (
                <ul className="mini-list">
                  {topIssues.map(f => (
                    <li key={f.id}>
                      <button type="button" className="mini-row" onClick={() => navigate('/issues', { id: f.id })}>
                        <SeverityBadge severity={f.severity} />
                        <span className="truncate grow">{f.title}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="card-body muted small">No open issue in the code that runs counts as work. What analysis finds appears here.</div>
              )}
              {upkeep ? (
                <div className="card-foot">
                  <button type="button" className="link-btn small" onClick={() => navigate('/issues', { scope: 'housekeeping' })}>
                    {plural(upkeep, 'housekeeping issue')} in tests
                  </button>
                </div>
              ) : null}
            </div>
            <div className="card">
              <div className="card-head">
                <Sparkles size={15} aria-hidden />
                <span className="strong">Project analysis</span>
                <span className="spacer" />
                <button type="button" className="link-btn small" onClick={() => navigate('/analysis')}>
                  Dashboard
                </button>
              </div>
              <div className="card-body stack">
                {coverage ? (
                  <div className="stat-row">
                    <Stat label="Fresh" value={coverage.fresh ?? 0} />
                    <Stat label="Stale" value={coverage.stale ?? 0} />
                    <Stat label="Missing" value={coverage.missing ?? 0} />
                  </div>
                ) : null}
                {run ? (
                  <div className="stack-tight">
                    <div className="row small">
                      <Badge tone={statusTone(run.status)}>{statusLabel(run.status)}</Badge>
                      <span className="muted">{runActive(run) ? 'running now' : run.updated_at ? timeAgo(run.updated_at) : ''}</span>
                      <span className="spacer" />
                      {progress.total ? <span className="xs muted">{progress.percent}%</span> : null}
                    </div>
                    {progress.total ? <Progress value={progress.percent} tone={progress.failed ? 'warning' : 'accent'} label="Analysis progress" /> : null}
                    {progress.failed ? <button type="button" className="link-btn xs tone-danger" onClick={() => navigate('/analysis')}>{plural(progress.failed, 'stage')} failed — see why</button> : null}
                  </div>
                ) : (
                  <p className="small muted">Analysis finds bugs, performance and security issues and writes the project docs. Start it from its page.</p>
                )}
              </div>
            </div>
            <EngineeringGrowthCard />
          </div>
        </div>
      </div>
    </div>
  );
}

function EngineeringGrowthCard() {
  const lessons = useLessons().data?.lessons ?? [];
  const mastery = useMemo(() => computeAtlasMastery(lessons), [lessons]);
  return (
    <div className="card">
      <div className="card-head">
        <GraduationCap size={15} aria-hidden />
        <span className="strong">Engineering growth</span>
        <span className="spacer" />
        <button type="button" className="link-btn small" onClick={() => navigate('/docs/atlas')}>
          Atlas
        </button>
      </div>
      <div className="card-body stack">
        <div className="row xs">
          <span className="muted">CS & system design models met</span>
          <span className="spacer" />
          <span className="mono strong">{mastery.metCurated} / {mastery.totalCurated}</span>
        </div>
        <Progress value={(mastery.metCurated / Math.max(1, mastery.totalCurated)) * 100} tone="accent" label="Engineering Atlas breadth" />
        <div className="row xs muted">
          <button type="button" className="link-btn xs" onClick={() => navigate('/docs/log')}>
            {plural(lessons.length, 'log entry', 'log entries')}
          </button>
          {mastery.calibration.written > 0 ? (
            <>
              <span>·</span>
              <span>{plural(mastery.calibration.written, 'own plan')} calibrated</span>
            </>
          ) : null}
        </div>
      </div>
    </div>
  );
}

/** Offers setup while the project has no configuration of its own, or a broken one. */
function SetupNotice() {
  const setup = useSetup().data;
  if (setup?.state === 'invalid') {
    return (
      <Callout tone="danger" icon={Wrench} title="The project’s configuration could not be applied" action={<Button size="sm" onClick={() => navigate('/setup')}>Fix in setup</Button>}>
        {setup.error} Kogin uses the daemon’s configuration until it is fixed.
      </Callout>
    );
  }
  if (setup?.state !== 'needed') return null;
  return (
    <Callout tone="info" icon={Wrench} title="Set up this project" action={<Button size="sm" variant="primary" onClick={() => navigate('/setup')}>Start setup</Button>}>
      Choose its models, agents, checks and what analysis reads. It uses the daemon’s configuration until then.
    </Callout>
  );
}

function inboxSummary(needs: number, running: number, failed: number, toMerge: number): string {
  const parts = [
    needs ? `${plural(needs, 'task')} ${needs === 1 ? 'needs' : 'need'} you` : '',
    running ? `${plural(running, 'agent run')} in progress` : '',
    failed ? `${failed} failed or stale` : '',
    toMerge ? `${plural(toMerge, 'branch', 'branches')} ready to merge` : '',
  ].filter(Boolean);
  return parts.length ? parts.join(' · ') : 'Nothing is waiting for you.';
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="stat">
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}
