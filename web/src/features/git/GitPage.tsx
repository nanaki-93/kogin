// Branches and worktrees: where agent work lives, how it relates to the main
// branch, and a merge preview before integrating it.

import { useEffect, useMemo, useState } from 'react';
import { AlertTriangle, FlaskConical, GitBranch, GitCommitHorizontal, GitMerge, ShieldOff, Trash2, X } from 'lucide-react';
import { useGitWorkspace, useMainProject, useTrust } from '@/api/hooks';
import type { GitWorktree, MergePreview } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { navigate, setParam, useRoute } from '@/lib/router';
import { parseGitDiff } from '@/lib/diff';
import { parseCommits, worktreeDiscardable, worktreeRemovable, worktreeTasks } from '@/lib/workflow';
import { basename, languageOf, plural, shortHash } from '@/lib/util';
import { Badge, Button, Callout, EmptyState, IconButton, Skeleton, Spinner, Tip, cx } from '@/components/ui';
import { TaskStatusBadge } from '@/components/domain';
import type { Tone } from '@/lib/util';
import { DiffView } from '@/features/review/DiffView';
import { CheckList } from '@/features/review/ChecksPanel';
import { setUI, useUI } from '@/app/store';
import { dismissMergeChecks, useMergeChecks } from '@/app/mergeChecks';

export function GitPage() {
  const route = useRoute();
  const { data: git, isLoading, error } = useGitWorkspace();
  const main = useMainProject().data;
  const trust = useTrust().data;
  const actions = useActions();
  const busy = useBusy();
  const trees = (git?.worktrees ?? []).filter(t => !t.current);
  const removeOnMerge = useUI(s => s.removeWorktreeOnMerge);
  const selectedPath = route.params.get('path') ?? trees[0]?.path ?? '';
  const selected = trees.find(t => t.path === selectedPath) ?? null;

  if (isLoading) return <div className="page-scroll"><div className="page-inner"><Skeleton lines={6} /></div></div>;
  if (error || !git?.available) return <EmptyState icon={GitBranch} title="Git is not available">This project is not a Git repository, or Git could not be read.</EmptyState>;

  return (
    <div className="split-page">
      <div className="split-main page-scroll">
        <div className="page-inner stack">
          <div className="page-head">
            <div className="grow">
              <h1 className="page-title">Branches</h1>
              <p className="page-sub">Each task works in its own Git worktree. Review its branch here and merge it into <span className="mono">{git.branch}</span>.</p>
            </div>
          </div>
          <Tip label="Applies to every merge. Off: the worktree and its branch stay after the merge, and Remove worktree deletes them later.">
            <label className="check-label">
              <input type="checkbox" checked={removeOnMerge} onChange={e => setUI({ removeWorktreeOnMerge: e.target.checked })} />
              Delete the worktree and its branch after merging
            </label>
          </Tip>
          {git.merge_head ? (
            <Callout tone="danger" icon={AlertTriangle} title="A merge is in progress" action={main ? <Button variant="danger" size="sm" onClick={() => void actions.abortMerge(git, main)} disabled={Boolean(busy)}>Abort merge</Button> : undefined}>
              {git.conflicts.length ? `Resolve ${plural(git.conflicts.length, 'conflicted file')} in your editor and commit, or abort: ${git.conflicts.slice(0, 4).join(', ')}` : 'Finish the merge in your editor and commit, or abort it.'}
            </Callout>
          ) : null}
          <MergeChecksCard />
          <div className="card">
            <div className="card-head">
              <GitBranch size={15} aria-hidden />
              <span className="strong">{git.branch}</span>
              <Badge>main checkout</Badge>
              <span className="spacer" />
              <span className="mono xs faint">{shortHash(git.head)}</span>
              {git.dirty ? <Badge tone="warning">uncommitted changes</Badge> : null}
            </div>
          </div>
          <div className="list-group-head">Worktrees <span className="count">{trees.length}</span></div>
          {!trees.length ? (
            <EmptyState icon={GitMerge} title="No worktrees">When agents work in isolation they get their own worktree and branch, listed here to review and merge.</EmptyState>
          ) : (
            <div role="list">
              {trees.map(tree => (
                <button key={tree.path} type="button" role="listitem" className={cx('list-row', tree.path === selectedPath && 'is-active')} onClick={() => setParam('path', tree.path)}>
                  <GitBranch size={14} className={tree.mergeable ? 'tone-warning' : 'faint'} aria-hidden />
                  <span className="list-title truncate">{tree.branch || basename(tree.path)}</span>
                  <span className="spacer" />
                  <IntegrationBadge tree={tree} />
                  <span className="xs muted">{plural(worktreeTasks(tree).length, 'task')}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
      {selected && main ? <WorktreeDetail tree={selected} trusted={Boolean(trust?.trusted)} /> : null}
    </div>
  );
}

const MERGE_OUTCOME: Record<string, { label: string; tone: Tone }> = {
  passed: { label: 'Passed', tone: 'success' },
  failed: { label: 'Failed', tone: 'danger' },
  unavailable: { label: 'Could not run', tone: 'warning' },
};

/**
 * The tests and lint of the subprojects the last merge changed, run on the
 * merged result; known failures from before the merge do not fail it.
 */
function MergeChecksCard() {
  const checks = useMergeChecks();
  if (!checks) return null;
  const outcome = checks.result ? MERGE_OUTCOME[checks.result.outcome] ?? { label: checks.result.outcome, tone: 'neutral' as Tone } : null;
  return (
    <section className="card" aria-label="Checks after the merge">
      <div className="card-head">
        <FlaskConical size={15} aria-hidden />
        <span className="strong grow truncate">Checks after merging {checks.branch}</span>
        {checks.status === 'running' ? <Spinner size={14} /> : null}
        {outcome ? <Badge tone={outcome.tone}>{outcome.label}</Badge> : null}
        {checks.status !== 'running' ? <IconButton icon={X} label="Dismiss the merge checks" size="sm" onClick={dismissMergeChecks} /> : null}
      </div>
      <div className="card-body stack">
        {checks.status === 'running' ? <p className="muted small">Running the tests and lint of the subprojects the merge changed on the merged result…</p> : null}
        {checks.status === 'error' ? <p className="small tone-danger">The checks could not run: {checks.error}</p> : null}
        {checks.result ? (
          <>
            <p className="small">{checks.result.summary}</p>
            <CheckList checks={checks.result.checks} />
          </>
        ) : null}
      </div>
    </section>
  );
}

/**
 * The worktree's relation to the main branch, as the daemon's gitops reports
 * it. `merged` means the main branch has every commit of the worktree, which
 * is also true of a new worktree with no commits yet; `up_to_date` means the
 * worktree is ahead.
 */
function integrationInfo(tree: GitWorktree): { label: string; tone: Tone } {
  if (tree.error) return { label: 'Unavailable', tone: 'danger' };
  if (tree.dirty) return { label: 'Uncommitted changes', tone: 'warning' };
  if (tree.integration === 'merged') return (tree.completed ?? []).length ? { label: 'Merged', tone: 'success' } : { label: 'No new commits', tone: 'neutral' };
  if (tree.integration === 'unrelated') return { label: 'Unrelated history', tone: 'danger' };
  if (tree.integration === 'behind') return { label: (tree.overlap_paths ?? []).length ? plural(tree.overlap_paths!.length, 'overlapping file') : 'Main moved ahead', tone: 'warning' };
  if (tree.mergeable) return { label: 'Ready to merge', tone: 'accent' };
  return { label: 'Work in progress', tone: 'neutral' };
}

function IntegrationBadge({ tree }: { tree: GitWorktree }) {
  const info = integrationInfo(tree);
  return <Badge tone={info.tone}>{info.label}</Badge>;
}

function notMergeableReason(tree: GitWorktree): string {
  if (tree.dirty) return 'Commit or discard the uncommitted changes in this worktree before merging.';
  if (tree.integration === 'merged') return (tree.completed ?? []).length ? 'This branch is already part of the main branch.' : 'This branch has no commits that the main branch lacks yet.';
  if (!worktreeTasks(tree).length) return 'No task is left on this branch to merge. Discard it with its work, or merge it with Git.';
  return 'Merge becomes available when the tasks on this branch are applied and committed.';
}

function WorktreeDetail({ tree, trusted }: { tree: GitWorktree; trusted: boolean }) {
  const git = useGitWorkspace().data!;
  const main = useMainProject().data!;
  const actions = useActions();
  const busy = useBusy();
  const layout = useUI(s => s.diffLayout);
  const [preview, setPreview] = useState<MergePreview | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let live = true;
    setPreview(null);
    if (!tree.mergeable) return;
    setLoading(true);
    void actions.previewMerge(git, tree, main).then(result => {
      if (live) {
        setPreview(result);
        setLoading(false);
      }
    });
    return () => {
      live = false;
    };
  }, [tree.path, tree.head]); // eslint-disable-line react-hooks/exhaustive-deps

  const files = useMemo(() => parseGitDiff(preview?.diff), [preview]);
  const commits = parseCommits(preview?.commits);
  const tasks = worktreeTasks(tree);

  return (
    <aside className="detail-pane detail-wide" aria-label="Worktree">
      <div className="detail-head">
        <GitBranch size={15} aria-hidden />
        <span className="strong">{tree.branch || basename(tree.path)}</span>
        <IntegrationBadge tree={tree} />
        <span className="spacer" />
        {tree.mergeable ? (
          <Button variant="primary" icon={GitMerge} onClick={() => void actions.merge(git, tree, main)} disabled={!trusted || Boolean(busy) || Boolean(git.merge_head)} loading={busy === 'Merging…'}>
            Merge into {git.branch}
          </Button>
        ) : worktreeRemovable(tree) ? (
          <Button variant="secondary" icon={Trash2} onClick={() => void actions.removeWorktree(git, tree, main)} disabled={!trusted || Boolean(busy) || Boolean(git.merge_head)} loading={busy === 'Removing worktree…'}>
            Remove worktree
          </Button>
        ) : worktreeDiscardable(tree) ? (
          <Button variant="secondary" icon={Trash2} onClick={() => void actions.discardWorktree(git, tree, main)} disabled={!trusted || Boolean(busy) || Boolean(git.merge_head)} loading={busy === 'Discarding worktree…'}>
            Discard worktree
          </Button>
        ) : null}
      </div>
      <div className="xs faint mono truncate">{tree.path}</div>
      {!tree.mergeable ? <p className="small muted">{notMergeableReason(tree)}</p> : null}
      {!trusted && (tree.mergeable || worktreeRemovable(tree) || worktreeDiscardable(tree)) ? <Callout tone="warning" icon={ShieldOff} title={tree.mergeable ? 'Merging needs trust' : 'Removing needs trust'}>Git can run repository filters and merge drivers. Allow checks in Settings first.</Callout> : null}
      {tree.integration === 'behind' && (tree.overlap_paths ?? []).length ? (
        <Callout tone="warning" icon={AlertTriangle} title={`${plural(tree.overlap_paths!.length, 'file')} changed on both branches`}>{tree.overlap_paths!.join(', ')}</Callout>
      ) : null}
      {tasks.length ? (
        <div>
          <div className="label-row">Tasks on this branch</div>
          {tasks.map(t => (
            <button key={t.id} type="button" className="mini-row" onClick={() => navigate(`/tasks/${t.id}`, { wt: tree.path })}>
              <span className="truncate grow">{t.title}</span>
              <TaskStatusBadge entry={t} />
            </button>
          ))}
        </div>
      ) : null}
      {loading ? <div className="row small muted"><Spinner size={14} /> Preparing merge preview…</div> : null}
      {commits.length ? (
        <div>
          <div className="label-row">{plural(commits.length, 'commit')}</div>
          <ul className="commits">{commits.map(c => (<li key={c.sha}><GitCommitHorizontal size={13} aria-hidden /><span className="mono xs faint">{c.sha.slice(0, 7)}</span><span className="small truncate">{c.subject}</span></li>))}</ul>
        </div>
      ) : null}
      {files.map(file => (
        <div key={file.path} className="merge-file">
          <div className="fc-diff-head"><span className="strong small truncate">{file.path}</span></div>
          {file.binary ? <div className="small muted">Binary file</div> : <DiffView lines={file.lines} language={languageOf(file.path)} layout={layout} wrap={false} anchorPrefix={`m-${file.path}`} />}
        </div>
      ))}
    </aside>
  );
}
