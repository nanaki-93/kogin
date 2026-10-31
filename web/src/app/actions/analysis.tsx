// Analysis actions: preview, consent and start (or resume) a project
// analysis, then pause or cancel it.

import { api, withWorktree } from '@/api/client';
import type { AnalysisChecksPlan, AnalysisRunSummary, Project } from '@/api/types';
import { plural } from '@/lib/util';
import { ChecksPlanNote } from '@/features/analysis/RunChecks';
import type { ActionContext } from './context';

/**
 * One dispatch window's bounds: up to 64 files and 30 minutes, two attempts
 * per stage. A window that ends pauses the run; resume continues it.
 */
const ANALYSIS_LIMITS = { batch_files: 64, budget_seconds: 1800, max_attempts_per_stage: 2 };

export interface AnalysisOptions {
  /** Retry only stages that failed, went stale or were canceled. */
  retryFailed?: boolean;
  /** Analyze again even where saved results are current. */
  refresh?: boolean;
  /** Resume this paused or interrupted run. */
  resume?: AnalysisRunSummary;
}

interface AnalysisPreview {
  identity: unknown;
  preview_id: string;
  providers?: { id: string; remote_confirmation_required?: boolean; model?: { model?: string; scope?: string; provider_origin?: string } }[];
  /** Retained files are outside a retry's scope: they keep their saved results. */
  files?: { retained?: boolean }[];
  max_model_requests?: number;
  /** The docs refresh that ends the run: its own bound on requests. */
  docs?: { max_model_requests?: number };
  /** The project checks after the files: the commands they run. */
  checks?: AnalysisChecksPlan;
  security_review_intent_required?: boolean;
}

export function analysisActions(context: ActionContext) {
  const { client, confirm, toast, guarded, fail } = context;

  /** Previews an analysis (or the resumption of a stopped run), asks for consent, then starts it. */
  async function runAnalysis(project: Project, worktreePath: string, options: AnalysisOptions = {}) {
    const resume = options.resume?.identity;
    await guarded(resume ? 'Preparing to resume…' : 'Preparing analysis…', async () => {
      try {
        // A resumption repeats the run's own plan options.
        const plan = options.resume?.plan;
        const limits = plan?.limits ?? ANALYSIS_LIMITS;
        const refresh = resume ? Boolean(plan?.refresh) : Boolean(options.refresh);
        const retryFailed = resume ? Boolean(plan?.retry_stale_failed) : Boolean(options.retryFailed);
        const preview = await api.post<AnalysisPreview>(withWorktree('/api/projects/current/analysis/preview', worktreePath), {
          project_id: project.project_id,
          project_revision: project.project_revision,
          scope: 'project',
          refresh,
          retry_stale_failed: retryFailed || undefined,
          resume_run: resume,
          limits,
        });
        const providers = preview.providers ?? [];
        const remote = providers.filter(p => p.remote_confirmation_required).map(p => `${p.model?.model || p.model?.scope} at ${p.model?.provider_origin || 'remote provider'}`);
        const security = Boolean(preview.security_review_intent_required);
        const what = options.retryFailed ? 'Retries the failed, stale and canceled stages of' : options.refresh ? 'Analyzes again, ignoring saved results,' : resume ? 'Resumes the analysis of' : 'Analyzes';
        const files = preview.files ?? [];
        const kept = files.filter(file => file.retained).length;
        const docsRequests = preview.docs?.max_model_requests ?? 0;
        const docs = docsRequests ? `, then summarizes again the docs' components whose files changed (up to ${plural(docsRequests, 'more request')})` : '';
        const ok = await confirm({
          title: resume ? 'Resume the analysis?' : options.retryFailed ? 'Retry failed analysis?' : 'Analyze the project?',
          body: (
            <div className="stack">
              <p>{`${what} ${plural(files.length - kept, 'file')} (up to ${preview.max_model_requests ?? 0} model requests) for bugs, performance and security${docs}.${kept ? ` The other ${plural(kept, 'file')} keep their saved results.` : ''}${security ? ' Includes an AI security review.' : ''}${remote.length ? ` Sends bounded source context to ${remote.join(', ')}.` : ''}`}</p>
              <ChecksPlanNote plan={preview.checks} />
            </div>
          ),
          confirmLabel: resume ? 'Resume' : 'Analyze',
        });
        if (!ok) return;
        const confirmations = { provider_ids: providers.map(p => p.id).filter(Boolean), security_review: security };
        if (resume) {
          await api.post(withWorktree('/api/projects/current/analysis/run/control', worktreePath), { identity: resume, action: 'resume', preview_id: preview.preview_id, confirmations });
        } else {
          await api.post(withWorktree('/api/projects/current/analysis/run', worktreePath), { identity: preview.identity, preview_id: preview.preview_id, limits, refresh, retry_stale_failed: retryFailed || undefined, confirmations });
        }
        toast(resume ? 'Analysis resumed.' : 'Analysis started.', 'info');
        void client.invalidateQueries({ queryKey: ['overview'] });
      } catch (err) {
        fail(resume ? 'Analysis could not resume' : 'Analysis could not start', err);
      }
    });
  }

  /** Pauses or cancels the running analysis; finished stages keep their results. */
  async function controlAnalysis(run: AnalysisRunSummary, worktreePath: string, action: 'pause' | 'cancel') {
    if (!run.identity) return;
    if (action === 'cancel') {
      const ok = await confirm({ title: 'Cancel the analysis?', body: 'Running model requests stop. Finished files keep their results; a new analysis starts over for the rest.', confirmLabel: 'Cancel analysis', cancelLabel: 'Keep running', danger: true });
      if (!ok) return;
    }
    await guarded(action === 'pause' ? 'Pausing analysis…' : 'Canceling analysis…', async () => {
      try {
        await api.post(withWorktree('/api/projects/current/analysis/run/control', worktreePath), { identity: run.identity, action });
        toast(action === 'pause' ? 'Analysis pauses after the current requests.' : 'Analysis canceled.', 'info');
        void client.invalidateQueries({ queryKey: ['overview'] });
      } catch (err) {
        fail(action === 'pause' ? 'Could not pause the analysis' : 'Could not cancel the analysis', err);
      }
    });
  }

  return { runAnalysis, controlAnalysis };
}
