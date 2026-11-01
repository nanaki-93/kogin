// "Files changed": a file tree with review state beside the selected file's
// diff, like a pull request. `v` marks the file viewed and moves to the next.

import { useMemo } from 'react';
import { Check, Columns2, FileDiff, ListTree, MessageSquare, Rows3, WrapText } from 'lucide-react';
import type { FileChange, LoadedSession } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { setUI, useUI } from '@/app/store';
import { usePageKeys } from '@/app/shortcuts';
import { diffStats } from '@/lib/diff';
import { effectiveEdits, isFileReviewed, nextUnreviewedPath, proposalReadiness, reviewProgress, workflowActive } from '@/lib/readiness';
import { navigate, setParam, useRoute } from '@/lib/router';
import { basename, dirname, languageOf } from '@/lib/util';
import { Button, EmptyState, IconButton, Kbd, Progress, Segmented, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';
import { DiffView } from './DiffView';
import { SendComments, useFileComments } from './LineComments';

export function FilesChanged({ session }: { session: LoadedSession }) {
  const route = useRoute();
  const layout = useUI(s => s.diffLayout);
  const wrap = useUI(s => s.wrapLines);
  const since = route.params.get('since') === '1' && Boolean(session.last_reviewed_hash);
  const edits = useMemo(() => effectiveEdits(session, since), [session, since]);
  const current = edits.find(edit => edit.path === route.params.get('file')) ?? edits[0];
  const progress = reviewProgress(session);
  const ready = proposalReadiness(session);
  const actions = useActions();
  const busy = useBusy();
  const canMark = ready.editable && !since && Boolean(current) && !isFileReviewed(session, current.path) && (session.changes ?? []).some(edit => edit.path === current?.path);
  const canComment = ready.editable && !since && !workflowActive(session) && (session.changes ?? []).some(edit => edit.path === current?.path);
  const fileComments = useFileComments(session, current?.path ?? '', canComment);
  const commentCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const comment of session.comments ?? []) if (comment.hash === session.hash && !comment.sent_revision) counts.set(comment.path, (counts.get(comment.path) ?? 0) + 1);
    return counts;
  }, [session.comments, session.hash]);

  // A line from a reviewer's finding belongs to the file it came with.
  const focusLine = current && route.params.get('file') === current.path ? Number(route.params.get('line')) || undefined : undefined;
  const select = (path: string) => navigate(route.path, { ...Object.fromEntries(route.params.entries()), file: path, line: undefined }, { replace: true });

  async function markViewed() {
    if (!current || !canMark) return;
    const ok = await actions.markViewed(session, current.path);
    if (!ok) return;
    const next = nextUnreviewedPath({ ...session, reviewed_files: [...(session.reviewed_files ?? []), current.path], file_review_hash: session.hash }, edits, current.path);
    if (next) select(next);
  }

  usePageKeys(key => {
    const index = edits.findIndex(edit => edit.path === current?.path);
    if (key === 'v' && canMark) void markViewed();
    else if ((key === 'j' || key === ']') && index < edits.length - 1) select(edits[index + 1].path);
    else if ((key === 'k' || key === '[') && index > 0) select(edits[index - 1].path);
    else if (key === 's') setUI({ diffLayout: layout === 'split' ? 'unified' : 'split' });
    else return false;
    return true;
  });

  if (!edits.length) {
    return (
      <EmptyState icon={FileDiff} title={since ? 'Nothing changed since your last review' : 'No changes yet'}>
        {since ? 'The proposal matches the version you reviewed.' : 'When agents write the change, its files appear here for review.'}
      </EmptyState>
    );
  }

  return (
    <div className="files-changed">
      <aside className="fc-tree" aria-label="Changed files">
        <div className="fc-tree-head">
          <div className="row">
            <ListTree size={14} aria-hidden />
            <span className="strong small">
              {progress.reviewed}/{progress.total} viewed
            </span>
            <span className="spacer" />
            {session.last_reviewed_hash ? (
              <Segmented
                label="Diff base"
                value={since ? 'since' : 'all'}
                onChange={value => setParam('since', value === 'since' ? '1' : undefined)}
                options={[
                  { value: 'all', label: 'All' },
                  { value: 'since', label: 'Since review' },
                ]}
              />
            ) : null}
          </div>
          <Progress value={progress.percent} tone={progress.reviewed === progress.total ? 'success' : 'accent'} label="Files viewed" />
          <SendComments session={session} disabled={!ready.editable || workflowActive(session)} />
        </div>
        <ul className="fc-files">
          {edits.map(edit => {
            const stats = diffStats(edit);
            const viewed = isFileReviewed(session, edit.path);
            return (
              <li key={edit.path}>
                <button type="button" className={cx('fc-file', edit.path === current?.path && 'is-active', viewed && 'is-viewed')} onClick={() => select(edit.path)} title={edit.path}>
                  <span className={cx('fc-check', viewed && 'is-on')} aria-label={viewed ? 'Viewed' : 'Not viewed'}>
                    {viewed ? <Check size={11} aria-hidden /> : null}
                  </span>
                  <FileIcon path={edit.path} />
                  <span className="grow truncate">
                    <span className="fc-name">{basename(edit.path)}</span>
                    <span className="fc-dir">{dirname(edit.path)}</span>
                  </span>
                  {commentCounts.get(edit.path) ? (
                    <span className="fc-comments xs" aria-label={`${commentCounts.get(edit.path)} open comments`}>
                      <MessageSquare size={11} aria-hidden /> {commentCounts.get(edit.path)}
                    </span>
                  ) : null}
                  <span className="fc-stats">
                    <span className="add">+{stats.added}</span>
                    <span className="del">−{stats.removed}</span>
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
        <div className="fc-tree-foot xs faint">
          <Kbd>j</Kbd>/<Kbd>k</Kbd> files · <Kbd>v</Kbd> viewed · <Kbd>s</Kbd> split
        </div>
      </aside>
      {current ? (
        <section className="fc-diff" aria-label={`Diff of ${current.path}`}>
          <div className="fc-diff-head">
            <FileIcon path={current.path} />
            <span className="truncate">
              <span className="faint">{dirname(current.path) ? dirname(current.path) + '/' : ''}</span>
              <span className="strong">{basename(current.path)}</span>
            </span>
            <FileStatus change={current} />
            <DiffCounts lines={current.diff?.lines ?? []} />
            <span className="spacer" />
            <IconButton icon={WrapText} label="Wrap lines" size="sm" active={wrap} onClick={() => setUI({ wrapLines: !wrap })} />
            <IconButton icon={layout === 'split' ? Rows3 : Columns2} label={layout === 'split' ? 'Unified view (s)' : 'Split view (s)'} size="sm" onClick={() => setUI({ diffLayout: layout === 'split' ? 'unified' : 'split' })} />
            {isFileReviewed(session, current.path) ? (
              <span className="badge badge-success">
                <Check size={12} aria-hidden /> Viewed
              </span>
            ) : (
              <Button size="sm" icon={Check} onClick={() => void markViewed()} disabled={!canMark || Boolean(busy)} kbd="V">
                Mark viewed
              </Button>
            )}
          </div>
          {current.summary ? <div className="fc-summary small">{current.summary}</div> : null}
          {current.criteria?.length ? (
            <div className="fc-criteria xs">
              <span className="muted">The agent says this file addresses:</span>{' '}
              {current.criteria.map(n => session.acceptance_criteria[n - 1]).filter(Boolean).join(' · ')}
            </div>
          ) : null}
          <div className="fc-diff-body">
            <DiffView key={current.path + session.hash + layout} lines={current.diff?.lines ?? []} language={languageOf(current.path)} layout={layout} wrap={wrap} anchorPrefix={`d-${current.path}`} comments={since ? undefined : fileComments} focus={focusLine} />
          </div>
        </section>
      ) : null}
    </div>
  );
}

/** Says when a file is new, deleted or moved; a modified file needs no label. */
function FileStatus({ change }: { change: FileChange }) {
  if (change.status === 'added') return <span className="badge badge-success">new</span>;
  if (change.status === 'deleted') return <span className="badge badge-danger">deleted</span>;
  if (change.status === 'renamed') return <span className="badge badge-info" title={`Renamed from ${change.old_path}`}>renamed from {change.old_path}</span>;
  return null;
}

function DiffCounts({ lines }: { lines: { kind: string }[] }) {
  const stats = diffStats({ lines: lines as never });
  const total = stats.added + stats.removed || 1;
  const blocks = 5;
  const add = Math.round((stats.added / total) * blocks);
  return (
    <span className="diff-counts" aria-label={`${stats.added} additions, ${stats.removed} deletions`}>
      <span className="add">+{stats.added}</span>
      <span className="del">−{stats.removed}</span>
      <span className="diff-blocks" aria-hidden>
        {Array.from({ length: blocks }, (_, i) => (
          <span key={i} className={i < add ? 'b-add' : i < blocks && stats.removed ? 'b-del' : 'b-none'} />
        ))}
      </span>
    </span>
  );
}

