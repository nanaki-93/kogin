// User actions against the daemon. Every mutation runs at most once at a time,
// carries the captured project/proposal identity, asks for explicit consent
// where the daemon requires `confirm`, and refreshes what it changed.
// Each area's actions live in ./actions, built from one shared context;
// useActions puts them together.

import { useActionContext } from './actions/context';
import { taskActions } from './actions/tasks';
import { reviewActions } from './actions/review';
import { projectActions } from './actions/project';
import { issueActions } from './actions/issues';
import { analysisActions } from './actions/analysis';
import { setupActions } from './actions/setup';
import { gitActions } from './actions/git';
import { docsActions } from './actions/docs';

export { useBusy, usePendingRevision, type PendingRevision } from './actions/busy';
export type { AnalysisOptions } from './actions/analysis';
export type { DeletableTask, DraftTask } from './actions/tasks';

export function useActions() {
  const context = useActionContext();
  const { guarded } = context;
  const { previewMerge, merge, removeWorktree, removeEmptyWorktrees, discardWorktree, discardAbandonedWorktrees, abortMerge } = gitActions(context);
  const { createTask, startAgents, reanalyze, retake, cancelAgents, refine, stopRevision, deleteTasks } = taskActions(context, { removeEmptyWorktrees, discardAbandonedWorktrees });
  const { markViewed, runChecks, addComment, deleteComment, approve, overrideReview, reject, apply } = reviewActions(context);
  const { setTrust, reindex, openProject, deleteSetAside } = projectActions(context);
  const { fixIssue, fixIssues, triageFinding } = issueActions(context);
  const { runAnalysis, controlAnalysis } = analysisActions(context);
  const { saveSetup, saveTimeouts, skipSetup, testSetupModel, saveAnalysisSelection, controlAgentSettings } = setupActions(context);
  const { refreshDocs, askCodebase } = docsActions(context);

  return { guarded, createTask, startAgents, reanalyze, retake, cancelAgents, refine, stopRevision, markViewed, runChecks, addComment, deleteComment, approve, overrideReview, reject, apply, setTrust, reindex, openProject, deleteSetAside, fixIssue, fixIssues, triageFinding, deleteTasks, saveSetup, saveTimeouts, skipSetup, testSetupModel, saveAnalysisSelection, runAnalysis, controlAnalysis, controlAgentSettings, previewMerge, merge, removeWorktree, discardWorktree, abortMerge, refreshDocs, askCodebase };
}
