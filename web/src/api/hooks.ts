// Server state. Every read goes through react-query so views share one cache,
// refetch on daemon events and never show another checkout's data.

import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { api, query, withWorktree } from './client';
import type {
  AgentSettings, AnalysisFileSelection, AvailableModels, ProjectSetup, ChangeHistoryEntry, ChangeRules, ChangeSession, DaemonStatus, DocsComponentFiles, ExecutionTrust, FileInfo, FileSummary,
  FindingsResponse, GitWorkspace, LoadedSession, ModelsCurrent, Overview, Project, ProjectIndex, ProjectRules, QuarantineReport, TaskEntry, UsageReport,
} from './types';
import { useCheckout } from '@/app/checkout';

export const keys = {
  status: ['status'] as const,
  models: ['models'] as const,
  availableModels: ['models', 'available'] as const,
  setup: ['setup'] as const,
  selection: ['selection'] as const,
  mainProject: ['project', ''] as const,
  project: (wt: string) => ['project', wt] as const,
  index: (wt: string) => ['index', wt] as const,
  trust: (wt: string) => ['trust', wt] as const,
  git: ['git'] as const,
  changes: (wt: string) => ['changes', wt] as const,
  session: (id: string) => ['session', id] as const,
  findings: (wt: string) => ['findings', wt] as const,
  overview: (wt: string) => ['overview', wt] as const,
  settings: (wt: string) => ['settings', wt] as const,
  file: (wt: string, path: string) => ['file', wt, path] as const,
  fileSummary: (wt: string, path: string) => ['fileSummary', wt, path] as const,
  docsComponent: (wt: string, path: string) => ['docsComponent', wt, path] as const,
  usage: (wt: string) => ['usage', wt] as const,
  quarantine: (wt: string) => ['quarantine', wt] as const,
  rules: (wt: string) => ['rules', wt] as const,
  changeRules: (id: string) => ['changeRules', id] as const,
};

/** The project's test file patterns and the AGENTS.md files that declare rules. */
export function useProjectRules() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.rules(worktreePath), project?.project_revision],
    queryFn: () => api.get<ProjectRules>(withWorktree('/api/projects/current/rules', worktreePath)),
    enabled: Boolean(project),
    staleTime: 60_000,
  });
}

/** A task's files measured against their budgets, for the proposal it holds now. */
export function useChangeRules(session: LoadedSession | undefined) {
  return useQuery({
    queryKey: [...keys.changeRules(session?.id ?? ''), session?.revision, session?.hash, session?.targets?.length],
    queryFn: () => {
      const { checkout } = session!;
      return api.get<ChangeRules>(withWorktree('/api/projects/current/changes/' + encodeURIComponent(session!.id) + '/rules' + query({ project_id: checkout.project_id, project_revision: checkout.project_revision }), checkout.worktreePath));
    },
    enabled: Boolean(session?.id),
    staleTime: 30_000,
    retry: false,
  });
}

/** Records the daemon set aside or skipped in the selected checkout. */
export function useQuarantine() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.quarantine(worktreePath), project?.project_revision],
    queryFn: () => api.get<QuarantineReport>(withWorktree('/api/projects/current/quarantine' + query({ project_id: project!.project_id, project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(project),
    staleTime: 60_000,
  });
}

/** What agents spent per task in the selected checkout. */
export function useUsage() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.usage(worktreePath), project?.project_revision],
    queryFn: () => api.get<UsageReport>(withWorktree('/api/projects/current/usage' + query({ project_id: project!.project_id, project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(project),
    staleTime: 15_000,
  });
}

export function useStatus() {
  return useQuery({ queryKey: keys.status, queryFn: () => api.get<DaemonStatus>('/status'), staleTime: 60_000, retry: false });
}

export function useModels() {
  return useQuery({ queryKey: keys.models, queryFn: () => api.get<ModelsCurrent>('/api/models/current'), staleTime: 60_000 });
}

/** The main project's setup: its own configuration file and whether setup is still needed. */
export function useSetup() {
  const project = useMainProject().data;
  return useQuery({
    queryKey: [...keys.setup, project?.project_revision],
    queryFn: () => api.get<ProjectSetup>('/api/projects/current/setup' + query({ project_id: project!.project_id, project_revision: project!.project_revision })),
    enabled: Boolean(project?.project_id),
    retry: false,
  });
}

/** Which files analysis leaves out. */
export function useAnalysisSelection(enabled: boolean) {
  const project = useMainProject().data;
  return useQuery({
    queryKey: [...keys.selection, project?.project_revision],
    queryFn: () => api.get<AnalysisFileSelection>('/api/projects/current/analysis/selection' + query({ project_id: project!.project_id, project_revision: project!.project_revision })),
    enabled: enabled && Boolean(project?.project_id),
  });
}

/** The model catalog. Listing Agy and Pi models runs their CLIs, so it loads only when asked. */
export function useAvailableModels(enabled: boolean) {
  return useQuery({ queryKey: keys.availableModels, queryFn: () => api.get<AvailableModels>('/api/models/available'), enabled, staleTime: 5 * 60_000, retry: false });
}

export function useMainProject() {
  return useQuery({ queryKey: keys.mainProject, queryFn: () => api.get<Project>('/api/projects/current'), retry: false, retryOnMount: false });
}

/** The project facts of the selected checkout (the main checkout or a worktree). */
export function useProject(): UseQueryResult<Project> {
  const { worktreePath } = useCheckout();
  return useQuery({ queryKey: keys.project(worktreePath), queryFn: () => api.get<Project>(withWorktree('/api/projects/current', worktreePath)), retry: false, retryOnMount: false });
}

function useRevision() {
  const project = useProject().data;
  return project?.project_id ? project : null;
}

export function useIndex() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.index(worktreePath), project?.project_revision],
    queryFn: () => api.get<ProjectIndex>(withWorktree('/api/projects/current/index', worktreePath)),
    enabled: Boolean(project),
    staleTime: 30_000,
  });
}

export function useTrust() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.trust(worktreePath), project?.project_revision],
    queryFn: () => api.get<ExecutionTrust>(withWorktree('/api/projects/current/execution-trust' + query({ project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(project),
  });
}

/** Execution trust of the main checkout, where every task's worktree starts. */
export function useMainTrust() {
  const main = useMainProject().data;
  return useQuery({
    queryKey: [...keys.trust(''), main?.project_revision],
    queryFn: () => api.get<ExecutionTrust>('/api/projects/current/execution-trust' + query({ project_revision: main!.project_revision })),
    enabled: Boolean(main?.project_id),
  });
}

export function useGitWorkspace() {
  const main = useMainProject().data;
  return useQuery({
    queryKey: [...keys.git, main?.project_revision],
    queryFn: () => api.get<GitWorkspace>('/api/projects/current/git/workspace' + query({ project_id: main!.project_id, project_revision: main!.project_revision })),
    enabled: Boolean(main?.project_id),
    retry: false,
  });
}

/** Tasks of the selected checkout, plus (from the main checkout) tasks living in other worktrees. */
export function useTasks(): UseQueryResult<ChangeHistoryEntry[]> & { tasks: TaskEntry[] } {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  const git = useGitWorkspace().data;
  const result = useQuery({
    queryKey: [...keys.changes(worktreePath), project?.project_revision],
    queryFn: () => api.get<ChangeHistoryEntry[]>(withWorktree('/api/projects/current/changes' + query({ project_id: project!.project_id, project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(project),
  });
  const tasks = mergeTaskSources(result.data ?? [], git, worktreePath);
  return Object.assign(result, { tasks });
}

export function mergeTaskSources(base: ChangeHistoryEntry[], git: GitWorkspace | undefined, worktreePath: string): TaskEntry[] {
  const out: TaskEntry[] = [];
  const seen = new Set<string>();
  for (const entry of base) {
    if (!entry?.id || seen.has(entry.id)) continue;
    seen.add(entry.id);
    out.push({ ...entry, worktreePath: entry.worktree_path || worktreePath });
  }
  if (!worktreePath && git) {
    for (const tree of git.worktrees ?? []) {
      if (tree.current || !tree.path) continue;
      for (const entry of tree.sessions ?? []) {
        if (!entry?.id || seen.has(entry.id)) continue;
        seen.add(entry.id);
        out.push({ ...entry, worktreePath: tree.path, branch: tree.branch });
      }
    }
  }
  return out.sort((a, b) => Date.parse(b.updated_at || '') - Date.parse(a.updated_at || ''));
}

async function checkoutIdentity(worktreePath: string) {
  const project = await api.get<Project>(withWorktree('/api/projects/current', worktreePath));
  return { project_id: project.project_id, project_revision: project.project_revision, worktreePath };
}

/** Loads one task with the identity of the checkout that owns it. */
export async function fetchSession(id: string, worktreePath: string): Promise<LoadedSession> {
  const checkout = await checkoutIdentity(worktreePath);
  const session = await api.get<ChangeSession>(withWorktree('/api/projects/current/changes/' + encodeURIComponent(id) + query({ project_id: checkout.project_id, project_revision: checkout.project_revision }), worktreePath));
  return { ...session, checkout };
}

export function useSession(id: string | undefined, worktreePath: string, live: boolean) {
  return useQuery({
    queryKey: keys.session(id ?? ''),
    queryFn: () => fetchSession(id!, worktreePath),
    enabled: Boolean(id),
    refetchInterval: query => (live || query.state.data?.workflow?.status === 'running' || query.state.data?.workflow?.status === 'canceling' ? 2500 : false),
  });
}

export function useFindings() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.findings(worktreePath), project?.project_revision],
    queryFn: () => api.get<FindingsResponse>(withWorktree('/api/projects/current/findings' + query({ project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(project),
  });
}

export function useOverview() {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.overview(worktreePath), project?.project_revision],
    queryFn: () => api.get<Overview>(withWorktree('/api/projects/current/overview' + query({ project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(project),
  });
}

export function useAgentSettings(worktreePath: string, identity: { project_id: string; project_revision: string } | null) {
  return useQuery({
    queryKey: [...keys.settings(worktreePath), identity?.project_revision],
    queryFn: () => api.get<AgentSettings>(withWorktree('/api/projects/current/agent-settings' + query({ project_id: identity!.project_id, project_revision: identity!.project_revision }), worktreePath)),
    enabled: Boolean(identity?.project_id),
    retry: false,
  });
}

/** A docs component's code files with a line on each from their saved summaries. Never calls a model. */
export function useDocsComponent(path: string | undefined) {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.docsComponent(worktreePath, path ?? ''), project?.project_revision],
    queryFn: () => api.get<DocsComponentFiles>(withWorktree('/api/projects/current/docs/component' + query({ path, project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(path && project),
    staleTime: 30_000,
    retry: false,
  });
}

/** The summary the last analysis run cached for a file. Reading it never calls a model. */
export function useFileSummary(path: string | undefined) {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.fileSummary(worktreePath, path ?? ''), project?.project_revision],
    queryFn: () => api.get<FileSummary>(withWorktree('/api/projects/current/files/analysis' + query({ path, project_revision: project!.project_revision }), worktreePath)),
    enabled: Boolean(path && project),
    staleTime: 30_000,
    retry: false,
  });
}

export function useFile(path: string | undefined) {
  const { worktreePath } = useCheckout();
  const project = useRevision();
  return useQuery({
    queryKey: [...keys.file(worktreePath, path ?? ''), project?.project_revision],
    queryFn: () => api.get<FileInfo>(withWorktree('/api/projects/current/files/info' + query({ path }), worktreePath)),
    enabled: Boolean(path && project),
    staleTime: 30_000,
    retry: false,
  });
}
