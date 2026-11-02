// Task actions: capture a task, run and stop its agents, revise it,
// re-analyze it when stale and delete it.

import { api, errorMessage, query, withWorktree } from '@/api/client';
import type { AgentSettings, AvailableModels, ChangeOpened, ChangeReanalysis, ChangeRetake, ChangeSession, FindingTarget, LoadedSession, Model, ModelsCurrent, Project, TaskIssue } from '@/api/types';
import { issueSteps } from '@/lib/issues';
import { changeIdentity, openComments, workflowActive } from '@/lib/readiness';
import { chooseRunModels, runStartOptions, workflowDestinations, workflowRequest, type DestinationChoice, type ModelStage } from '@/lib/workflow';
import { roleModelOptions, selectedModel, type ModelOptionGroup } from '@/lib/agentSettings';
import { navigate } from '@/lib/router';
import { openWorkspace } from '@/app/checkout';
import { commentsBrief, isTestPath, plural } from '@/lib/util';
import { ResumeChoice, RunStartChoice } from '@/components/domain';
import { keys } from '@/api/hooks';
import { pendingRevision, setPendingRevision } from './busy';
import { changePath, type ActionContext } from './context';

/** What deleting a task needs: the version shown and the checkout that owns it. */
export interface DeletableTask {
  id: string;
  title: string;
  worktreePath: string;
  project_id: string;
  project_revision: string;
  revision: number;
  hash: string;
}

export interface DraftTask {
  title: string;
  kind: string;
  description: string;
  paths: string[];
  criteria: string[];
  start: 'agents' | 'quick' | 'draft';
  /** The issues this task fixes; only those whose file stays in scope are sent. */
  findingTargets?: FindingTarget[];
  /** Several issues, fixed one at a time; sent for the linked ones when two or more remain. */
  issues?: TaskIssue[];
}

/** What deleting tasks does with the worktrees it leaves without tasks. */
interface LeftWorktrees {
  /** Removes those with nothing to merge; returns them. */
  removeEmptyWorktrees: (paths: string[]) => Promise<string[]>;
  /** Offers to discard those still holding work; returns the ones discarded. */
  discardAbandonedWorktrees: (paths: string[]) => Promise<string[]>;
}

export function taskActions(context: ActionContext, { removeEmptyWorktrees, discardAbandonedWorktrees }: LeftWorktrees) {
  const { client, confirm, toast, guarded, fail, setSession, confirmRemote } = context;

  // ---------------------------------------------------------------- tasks

  /**
   * Captures a task in a new branch and worktree the daemon creates for it
   * from the main checkout (`project` is the main checkout's identity).
   */
  async function createTask(project: Project, draft: DraftTask): Promise<string | null> {
    const linked = draft.findingTargets?.filter(target => draft.paths.includes(target.path));
    const created = await guarded('Creating task…', async () => {
      const body = {
        project_id: project.project_id,
        project_revision: project.project_revision,
        kind: draft.kind,
        title: draft.title.trim(),
        paths: draft.paths,
        acceptance_criteria: draft.criteria.length ? draft.criteria : ['Changes pass verification checks'],
        plan: draft.description.trim() ? { problem: draft.title.trim(), approach: draft.description.trim() } : undefined,
        finding_targets: linked,
        issues: issueSteps(draft.issues, linked),
        confirm_execution: true,
      };
      try {
        return await api.post<ChangeOpened>('/api/projects/current/changes', body);
      } catch (err) {
        fail('Could not create the task', err);
        return null;
      }
    });
    if (!created) return null;
    const checkout = created.worktree_path;
    const loaded: LoadedSession = { ...created, checkout: { project_id: created.project_id || project.project_id, project_revision: created.project_revision || project.project_revision, worktreePath: checkout } };
    client.setQueryData(keys.session(created.id), loaded);
    void client.invalidateQueries({ queryKey: ['changes'] });
    void client.invalidateQueries({ queryKey: ['git'] });
    // The issues it fixes are in progress now.
    if (created.finding_targets?.length) void client.invalidateQueries({ queryKey: ['findings'] });
    for (const warning of created.warnings ?? []) toast(warning, 'warning');
    navigate(`/tasks/${created.id}`, { wt: checkout || undefined });
    const prompt = draft.title.trim() + (draft.description.trim() ? `\n\nSpecification:\n${draft.description.trim()}` : '');
    if (draft.start === 'agents') await startAgents(loaded, { message: prompt });
    else if (draft.start === 'quick') await refine(loaded, prompt);
    else toast(`Draft saved. It has its own worktree on ${created.branch}.`, 'success');
    return created.id;
  }

  async function startAgents(session: LoadedSession, options: { message?: string } = {}): Promise<boolean> {
    const comments = openComments(session).length;
    const message = (options.message ?? session.title).trim();
    if (!message) {
      toast('Describe the change for the agents first.', 'warning');
      return false;
    }
    let settings: AgentSettings | null = null;
    try {
      settings = await api.get<AgentSettings>(withWorktree('/api/projects/current/agent-settings' + query({ project_id: session.checkout.project_id, project_revision: session.checkout.project_revision }), session.checkout.worktreePath));
    } catch {
      settings = null; // Daemon defaults apply; unknown destinations are never auto-confirmed.
    }
    const choice = workflowDestinations(settings, client.getQueryData<ModelsCurrent>(keys.models));
    const security = session.kind === 'security';
    const starts = runStartOptions(session, choice.selections.create, isTestPath);
    let start = starts.initial; // The choice below updates it while the dialog is open.
    const again = starts.options.length > 0;
    const pause = session.workflow?.status === 'paused' ? session.workflow.pause : undefined;
    const paused = Boolean(pause);
    // A run paused at a plan's limit can go on now with another model.
    const models = pause ? await modelOptions(settings, choice, session) : null;
    const picked: Partial<Record<ModelStage, string>> = {};
    const ok = await confirm({
      title: paused ? 'Resume the agents?' : again ? 'Run the agents again?' : 'Start the agents?',
      body: (
        <div className="stack">
          {models ? (
            <ResumeChoice
              starts={starts}
              comments={comments}
              previous={models.previous}
              groups={models.groups}
              initial={choice.selections}
              limited={pause?.provider}
              onStart={value => (start = value)}
              onModel={(stage, selection) => (picked[stage] = selection)}
            />
          ) : null}
          {again ? (
            <>
              {models ? null : <RunStartChoice options={starts.options} initial={starts.initial} comments={comments} onChange={value => (start = value)} />}
              <p className="small muted">Checks run in an isolated copy of the project. Nothing is committed until you review and accept the result. A failed check or a rejecting review starts an automatic repair of the code (up to two).</p>
            </>
          ) : (
            <p>Agents write the change, write and run tests in an isolated copy of the project, then specialist reviewers check it. Nothing reaches your files until you review and apply it.</p>
          )}
          {models ? null : choice.destinations.length ? (
            <div className="consent-list">
              <div className="small strong">Sends code to remote models</div>
              {choice.destinations.map(d => (
                <div key={d.stage} className="small">
                  <span className="muted">{d.stage}</span> · {d.model} <span className="faint">at {d.origin}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="small muted">All selected models run locally.</p>
          )}
          {security ? <p className="small">This is a security task: a security specialist reviews it.</p> : null}
          {comments && !again ? <p className="small">{commentsBrief(comments)}</p> : null}
        </div>
      ),
      confirmLabel: paused ? 'Resume agents' : again ? 'Run agents' : 'Start agents',
    });
    if (!ok) return false;
    return Boolean(
      await guarded('Starting agents…', async () => {
        try {
          const run = models ? chooseRunModels(choice, picked, start, session.workflow, models.modelOf) : choice;
          const body = workflowRequest(changeIdentity(session), message, run, { autoRepair: true, confirmSecurity: security, planHash: session.plan?.hash, includeComments: comments > 0 && start !== 'checks', start });
          setSession(await api.post<ChangeSession>(changePath(session, '/workflow'), body), session);
          toast('Agents started. Progress updates live.', 'info');
          return true;
        } catch (err) {
          fail('The agents could not start', err);
          return false;
        }
      }),
    );
  }

  /**
   * The models a resumed run can choose from: the configured scopes and what
   * the CLIs list (only the scopes when listing fails), and how each runs.
   */
  async function modelOptions(settings: AgentSettings | null, choice: DestinationChoice, session: LoadedSession): Promise<{ groups: ModelOptionGroup[]; previous: Partial<Record<ModelStage, string>>; modelOf: (selection: string) => Model | undefined }> {
    const scopes = client.getQueryData<ModelsCurrent>(keys.models)?.scopes ?? {};
    let catalog: AvailableModels | undefined;
    try {
      catalog = await client.fetchQuery({ queryKey: keys.availableModels, queryFn: () => api.get<AvailableModels>('/api/models/available'), staleTime: 5 * 60_000 });
    } catch {
      catalog = undefined;
    }
    const roleOf = { create: 'creation', test: 'testing', review: 'review' } as const;
    const known: Record<string, Model> = {};
    for (const stage of ['create', 'test', 'review'] as const) {
      const model = settings?.role_models?.[roleOf[stage]];
      if (model) known[choice.selections[stage]] = model;
    }
    // The model each stage ran with last time, which a stage the start keeps keeps.
    const previous: Partial<Record<ModelStage, string>> = {};
    for (const stage of session.workflow?.stages ?? []) {
      if ((stage.name === 'create' || stage.name === 'test' || stage.name === 'review') && stage.model?.profile) {
        previous[stage.name] = stage.model.profile;
        known[stage.model.profile] ??= stage.model;
      }
    }
    const groups = roleModelOptions(scopes, catalog, [...Object.values(choice.selections), ...Object.values(previous)], known);
    return { groups, previous, modelOf: selection => selectedModel(groups, selection) ?? known[selection] };
  }

  /**
   * Asks the Bug model whether a stale task's issue is still present in the
   * current files: a valid task is refreshed against them, the rest deleted.
   */
  async function reanalyze(session: LoadedSession): Promise<boolean> {
    const model = client.getQueryData<ModelsCurrent>(keys.models)?.scopes?.bug;
    const remote = Boolean(model?.remote_provider);
    const linked = session.finding_targets?.length ?? 0;
    const ok = await confirm({
      title: 'Re-analyze this task?',
      body: (
        <div className="stack">
          {session.stale_reason ? <p>Out of date: {session.stale_reason}.</p> : null}
          <p>A model checks whether the issue is still present in the current files.</p>
          <p className="small">
            <strong>Still valid:</strong> the task is refreshed against the current files and can run again; its old proposal, checks and conversation are dropped.
            <br />
            <strong>Not valid:</strong> the task is deleted{linked ? ` and its ${linked === 1 ? 'linked issue is' : `${linked} linked issues are`} dismissed` : ''}.
          </p>
          {remote ? <p className="small muted">Sends the task and its files to {model?.model || 'the Bug model'} at {model?.provider_origin || 'a remote provider'}.</p> : null}
        </div>
      ),
      confirmLabel: 'Re-analyze',
    });
    if (!ok) return false;
    const result = await guarded('Re-analyzing…', async () => {
      try {
        return await api.post<ChangeReanalysis>(changePath(session, '/reanalyze'), { ...changeIdentity(session), confirm_remote_provider: remote });
      } catch (err) {
        fail('Re-analysis failed', err);
        return null;
      }
    });
    if (!result) return false;
    if (result.outcome === 'still_valid' && result.session) {
      setSession(result.session, session);
      toast('Still valid: the task now matches the current files. Run the agents when ready.', 'success', result.reason);
    } else {
      openWorkspace('/tasks');
      void client.invalidateQueries({ queryKey: ['changes'] });
      void client.invalidateQueries({ queryKey: ['findings'] });
      const removed = await guarded('Removing its worktree…', () => removeEmptyWorktrees([session.checkout.worktreePath]));
      const dismissed = result.dismissed_issues ? ` and ${plural(result.dismissed_issues, 'linked issue')} dismissed` : '';
      toast(`No longer valid: “${session.title || session.id}” was deleted${dismissed}${removed?.length ? ', with its worktree' : ''}.`, 'info', result.reason);
      if (!removed?.length) await discardAbandonedWorktrees([session.checkout.worktreePath]);
    }
    for (const warning of result.warnings ?? []) toast(warning, 'warning');
    return true;
  }

  /**
   * Retakes a task in its own worktree whose files the main branch changed
   * since it started: the Bug model checks whether its issue is still present
   * in the main branch's files. Still valid, it starts again in a new
   * worktree from there; not valid, it is deleted and its linked issues
   * dismissed. Either way the old task is deleted, and its worktree is
   * removed, or discarded with its work if the user says so.
   */
  async function retake(session: LoadedSession): Promise<boolean> {
    const change = session.main_changed;
    if (!change) return false;
    const model = client.getQueryData<ModelsCurrent>(keys.models)?.scopes?.bug;
    const remote = Boolean(model?.remote_provider);
    const linked = session.finding_targets?.length ?? 0;
    const ok = await confirm({
      title: 'Re-analyze and retake this task?',
      body: (
        <div className="stack">
          <p>
            {change.branch} changed {change.paths.join(', ')} since this task started. A model checks whether the issue is still present in {change.branch}'s files.
          </p>
          <p className="small">
            <strong>Still valid:</strong> the task starts again in a new worktree from {change.branch}, with its title, plan, criteria, scope and linked issues; this one is deleted.
            <br />
            <strong>Not valid:</strong> the task is deleted{linked ? ` and its ${linked === 1 ? 'linked issue is' : `${linked} linked issues are`} dismissed` : ''}.
          </p>
          <p className="small muted">
            Either way you then decide whether its worktree and the work in it are discarded.
            {remote ? ` Sends the task and its files to ${model?.model || 'the Bug model'} at ${model?.provider_origin || 'a remote provider'}.` : ''}
          </p>
        </div>
      ),
      confirmLabel: 'Re-analyze & retake',
    });
    if (!ok) return false;
    const result = await guarded('Retaking…', async () => {
      try {
        return await api.post<ChangeRetake>(changePath(session, '/retake'), { ...changeIdentity(session), confirm_remote_provider: remote, confirm_execution: true });
      } catch (err) {
        fail('Retake failed', err);
        return null;
      }
    });
    if (!result) return false;
    for (const key of ['changes', 'git', 'findings']) void client.invalidateQueries({ queryKey: [key] });
    if (result.outcome === 'still_valid' && result.opened) {
      navigate(`/tasks/${result.opened.id}`, { wt: result.opened.worktree_path });
      toast(`Still valid: “${session.title || session.id}” starts again from ${change.branch} on ${result.opened.branch}. Run the agents when ready.`, 'success', result.reason);
    } else {
      openWorkspace('/tasks');
      const dismissed = result.dismissed_issues ? ` and ${plural(result.dismissed_issues, 'linked issue')} dismissed` : '';
      toast(`No longer valid: “${session.title || session.id}” was deleted${dismissed}.`, 'info', result.reason);
    }
    for (const warning of result.warnings ?? []) toast(warning, 'warning');
    if (result.deleted) {
      const removed = await guarded('Removing its worktree…', () => removeEmptyWorktrees([session.checkout.worktreePath]));
      if (!removed?.length) await discardAbandonedWorktrees([session.checkout.worktreePath]);
    }
    return true;
  }

  async function cancelAgents(session: LoadedSession) {
    if (!workflowActive(session) || !session.workflow) return;
    if (!(await confirm({ title: 'Stop the agents?', body: 'Running agents stop. Nothing is applied.', confirmLabel: 'Stop agents', cancelLabel: 'Keep running', danger: true }))) return;
    await guarded('Stopping agents…', async () => {
      try {
        setSession(await api.post<ChangeSession>(changePath(session, '/workflow/cancel'), { project_id: session.checkout.project_id, project_revision: session.checkout.project_revision, workflow_id: session.workflow!.id }), session);
        toast('Agents stopped.', 'info');
      } catch (err) {
        fail('Could not stop the agents', err);
      }
    });
  }

  /** Revises the proposal from a message and, when asked, the open line comments. */
  async function refine(session: LoadedSession, message: string, includeComments = false): Promise<boolean> {
    const text = message.trim();
    if (!text && !includeComments) {
      toast('Describe what should change first.', 'warning');
      return false;
    }
    const consent = await confirmRemote('code', `Revising “${session.title || session.id}”`);
    if (!consent.ok) return false;
    return Boolean(
      await guarded('Generating…', async () => {
        // The model call runs inside this request; stopping closes it, and the
        // daemon cancels the call and saves nothing.
        const controller = new AbortController();
        setPendingRevision({ sessionId: session.id, since: Date.now(), stop: () => controller.abort() });
        try {
          setSession(await api.post<ChangeSession>(changePath(session, '/messages'), { ...changeIdentity(session), message: text, profile: 'code', confirm_remote_provider: consent.remote, include_comments: includeComments || undefined }, controller.signal), session);
          toast('Proposal updated. Review the new diff.', 'success');
          return true;
        } catch (err) {
          if (controller.signal.aborted) {
            // A revision that finished as Stop was pressed is kept: show the saved version.
            void client.invalidateQueries({ queryKey: keys.session(session.id) });
            toast('Revision stopped.', 'info');
          } else {
            fail('Generation failed', err);
          }
          return false;
        } finally {
          setPendingRevision(null);
        }
      }),
    );
  }

  /** Stops waiting for the task's revision; the daemon then cancels the model call. */
  async function stopRevision(session: LoadedSession) {
    const pending = pendingRevision;
    if (pending?.sessionId !== session.id) return;
    if (!(await confirm({ title: 'Stop the revision?', body: 'The agent stops and this version stays as it is. Nothing is applied.', confirmLabel: 'Stop revision', cancelLabel: 'Keep waiting', danger: true }))) return;
    pending.stop();
  }

  // ------------------------------------------------------------------ deletion

  /**
   * Deletes tasks after one confirmation and returns how many were deleted.
   * `leave` runs first, so a page showing a deleted task navigates away
   * before the task list refreshes; its cached page expires unused.
   */
  async function deleteTasks(tasks: DeletableTask[], leave?: () => void): Promise<number> {
    if (!tasks.length) return 0;
    const one = tasks.length === 1;
    const worktrees = tasks.some(task => task.worktreePath) ? ' A worktree left with nothing to merge is removed with its branch.' : '';
    const ok = await confirm({
      title: one ? 'Delete this task?' : `Delete ${tasks.length} tasks?`,
      body: one
        ? `“${tasks[0].title || tasks[0].id}” and its conversation, proposals and comments are removed. Project files are not changed.${worktrees}`
        : `Their conversations, proposals and comments are removed. Project files are not changed.${worktrees} ${tasks.slice(0, 5).map(t => `“${t.title || t.id}”`).join(', ')}${tasks.length > 5 ? ` and ${tasks.length - 5} more` : ''}.`,
      confirmLabel: one ? 'Delete task' : `Delete ${tasks.length} tasks`,
      danger: true,
    });
    if (!ok) return 0;
    const done = await guarded(one ? 'Deleting task…' : 'Deleting tasks…', async () => {
      const deleted: string[] = [];
      const failures: string[] = [];
      for (const task of tasks) {
        try {
          await api.post(withWorktree('/api/projects/current/changes/' + encodeURIComponent(task.id) + '/delete', task.worktreePath), { project_id: task.project_id, project_revision: task.project_revision, revision: task.revision, hash: task.hash });
          deleted.push(task.id);
        } catch (err) {
          failures.push(`${task.title || task.id}: ${errorMessage(err)}`);
        }
      }
      if (deleted.length) leave?.();
      void client.invalidateQueries({ queryKey: ['changes'] });
      // Issues the deleted tasks were fixing are open again.
      void client.invalidateQueries({ queryKey: ['findings'] });
      const left = tasks.filter(task => deleted.includes(task.id)).map(task => task.worktreePath);
      const removed = await removeEmptyWorktrees(left);
      const worktreeNote = removed.length ? ` ${removed.length === 1 ? 'Its worktree was' : `${removed.length} worktrees were`} removed too.` : '';
      if (deleted.length) toast((one ? 'Task deleted.' : `${plural(deleted.length, 'task')} deleted.`) + worktreeNote, 'success');
      if (failures.length) toast(`Could not delete ${plural(failures.length, 'task')}`, 'danger', failures.join('\n'));
      return { deleted: deleted.length, left: left.filter(path => !removed.includes(path)) };
    });
    // A worktree still holding the deleted tasks' work, such as a branch whose
    // merge was aborted, is discarded only if the user says so.
    if (done?.left.length) await discardAbandonedWorktrees(done.left);
    return done?.deleted ?? 0;
  }

  /**
   * Closes a task whose agents found nothing to fix: the issues that do not
   * hold are dismissed with their reasons, the task is deleted and its
   * worktree, holding no work, is removed.
   */
  async function closeTask(session: LoadedSession, leave?: () => void): Promise<boolean> {
    const dismissed = (session.issues ?? []).filter(issue => issue.status === 'does_not_hold');
    const ok = await confirm({
      title: 'Close this task?',
      body: (
        <div className="stack">
          <p>
            {dismissed.length
              ? `${plural(dismissed.length, 'issue')} that ${dismissed.length === 1 ? 'does' : 'do'} not hold ${dismissed.length === 1 ? 'is' : 'are'} dismissed, with the reasons shown under Creation; you can reopen them in Issues.`
              : 'No issue is dismissed.'}{' '}
            The task is deleted; it changed no files.
          </p>
          {dismissed.length ? (
            <ul className="small">
              {dismissed.slice(0, 5).map(issue => (
                <li key={issue.finding_id}>{issue.title}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ),
      confirmLabel: 'Close task',
    });
    if (!ok) return false;
    const closed = await guarded('Closing task…', async () => {
      try {
        return await api.post<{ deleted: string; dismissed_issues: number }>(changePath(session, '/close'), changeIdentity(session));
      } catch (err) {
        fail('Could not close the task', err);
        return null;
      }
    });
    if (!closed) return false;
    leave?.();
    void client.invalidateQueries({ queryKey: ['changes'] });
    void client.invalidateQueries({ queryKey: ['findings'] });
    const removed = session.checkout.worktreePath ? await removeEmptyWorktrees([session.checkout.worktreePath]) : [];
    toast(`Task closed${closed.dismissed_issues ? `; ${plural(closed.dismissed_issues, 'issue')} dismissed` : ''}.${removed.length ? ' Its worktree was removed too.' : ''}`, 'success');
    return true;
  }

  return { createTask, startAgents, reanalyze, retake, cancelAgents, refine, stopRevision, deleteTasks, closeTask };
}
