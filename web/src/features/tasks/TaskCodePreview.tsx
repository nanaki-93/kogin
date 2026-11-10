// Inline read-only source preview inside a task: clicking any cited line,
// scope file or hunk file opens its full source beside the plan or diff
// without leaving the task. Reading never calls a model or writes files.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { ExternalLink, X } from 'lucide-react';
import { useFile, useFileSummary } from '@/api/hooks';
import type { LoadedSession } from '@/api/types';
import { usePageKeys } from '@/app/shortcuts';
import { highlightLines } from '@/lib/highlight';
import { navigate } from '@/lib/router';
import { basename, dirname, languageOf } from '@/lib/util';
import { Badge, IconButton, Segmented, Skeleton, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';

export interface TaskCodeTarget {
  path: string;
  startLine?: number;
  endLine?: number;
}

interface TaskCodeContextValue {
  target: TaskCodeTarget | null;
  openFile: (path: string, startLine?: number, endLine?: number) => void;
  closeFile: () => void;
}

const TaskCodeContext = createContext<TaskCodeContextValue | null>(null);

export function TaskCodePreviewProvider({ children }: { children: ReactNode }) {
  const [target, setTarget] = useState<TaskCodeTarget | null>(null);
  const openFile = useCallback((path: string, startLine?: number, endLine?: number) => {
    setTarget(prev => (prev?.path === path && prev.startLine === startLine && prev.endLine === endLine ? null : { path, startLine, endLine }));
  }, []);
  const closeFile = useCallback(() => setTarget(null), []);
  const value = useMemo(() => ({ target, openFile, closeFile }), [target, openFile, closeFile]);
  return <TaskCodeContext.Provider value={value}>{children}</TaskCodeContext.Provider>;
}

/** Opens a file inside the current task when inside TaskPage, else in Code. */
export function useOpenTaskFile(worktreePath?: string) {
  const ctx = useContext(TaskCodeContext);
  return useCallback(
    (path: string, startLine?: number, endLine?: number) => {
      if (ctx) {
        ctx.openFile(path, startLine, endLine);
        return;
      }
      navigate('/code', { file: path, line: startLine ? String(startLine) : undefined, wt: worktreePath || undefined });
    },
    [ctx, worktreePath],
  );
}

export function TaskCodePane({ session }: { session: LoadedSession }) {
  const ctx = useContext(TaskCodeContext);
  const target = ctx?.target ?? null;
  const proposed = useMemo(() => (target ? (session.changes ?? []).find(edit => edit.path === target.path) : undefined), [session.changes, target]);
  const [sourceMode, setSourceMode] = useState<'current' | 'proposed'>('current');

  useEffect(() => {
    setSourceMode(proposed && proposed.status === 'added' ? 'proposed' : 'current');
  }, [target?.path, proposed]);

  usePageKeys(key => {
    if (key === 'Escape' && target && ctx) {
      ctx.closeFile();
      return true;
    }
    return false;
  });

  if (!target || !ctx) return null;
  return (
    <aside className="task-code-pane" aria-label="Code preview">
      <PaneHeader
        session={session}
        target={target}
        hasProposed={Boolean(proposed?.content)}
        sourceMode={sourceMode}
        onModeChange={setSourceMode}
        onClose={ctx.closeFile}
      />
      <PaneBody target={target} proposedContent={sourceMode === 'proposed' ? proposed?.content : undefined} />
    </aside>
  );
}

function PaneHeader({ session, target, hasProposed, sourceMode, onModeChange, onClose }: {
  session: LoadedSession;
  target: TaskCodeTarget;
  hasProposed: boolean;
  sourceMode: 'current' | 'proposed';
  onModeChange: (mode: 'current' | 'proposed') => void;
  onClose: () => void;
}) {
  const dir = dirname(target.path);
  const rangeLabel = target.startLine ? `:${target.startLine}${target.endLine && target.endLine !== target.startLine ? `–${target.endLine}` : ''}` : '';
  return (
    <div className="task-code-head">
      <FileIcon path={target.path} size={14} />
      <div className="grow truncate" title={target.path}>
        <span className="strong small">{basename(target.path)}</span>
        {rangeLabel ? <span className="mono xs tone-accent">{rangeLabel}</span> : null}
        {dir ? <span className="xs faint task-code-dir">{dir}</span> : null}
      </div>
      {hasProposed ? (
        <Segmented
          label="Preview version"
          value={sourceMode}
          onChange={onModeChange}
          options={[
            { value: 'current', label: 'Current' },
            { value: 'proposed', label: 'Proposed' },
          ]}
        />
      ) : null}
      <IconButton
        icon={ExternalLink}
        label="Open in Code workspace"
        size="sm"
        onClick={() => navigate('/code', { file: target.path, line: target.startLine ? String(target.startLine) : undefined, wt: session.checkout.worktreePath || undefined })}
      />
      <IconButton icon={X} label="Close code preview" size="sm" onClick={onClose} />
    </div>
  );
}

function PaneBody({ target, proposedContent }: { target: TaskCodeTarget; proposedContent?: string }) {
  const { data: info, isLoading, error } = useFile(proposedContent !== undefined ? undefined : target.path);
  const summary = useFileSummary(target.path).data;
  const scroller = useRef<HTMLDivElement>(null);
  const content = proposedContent ?? info?.content ?? '';
  const html = useMemo(() => (content ? highlightLines(content, languageOf(target.path)) : []), [content, target.path]);
  const start = target.startLine ?? 0;
  const end = target.endLine ?? start;

  useEffect(() => {
    if (!start || !html.length) return;
    const row = scroller.current?.querySelector(`[data-line="${start}"]`);
    row?.scrollIntoView({ block: 'center' });
  }, [target.path, start, html.length, proposedContent]);

  return (
    <>
      {summary?.purpose ? (
        <div className="task-code-purpose xs muted">
          <Badge tone="neutral">Role</Badge>
          <span className="truncate" title={summary.purpose}>{summary.purpose}</span>
        </div>
      ) : null}
      <div className="task-code-scroll" ref={scroller}>
        {isLoading && proposedContent === undefined ? (
          <div className="card-body"><Skeleton lines={10} /></div>
        ) : error && proposedContent === undefined ? (
          <div className="card-body small muted">{error instanceof Error ? error.message : 'File unavailable in this checkout.'}</div>
        ) : !html.length ? (
          <div className="card-body small muted">Empty file.</div>
        ) : (
          <pre className="task-code-pre">
            {html.map((line, index) => {
              const lineNo = index + 1;
              const cited = start > 0 && lineNo >= start && lineNo <= end;
              return (
                <span key={lineNo} data-line={lineNo} className={cx('task-code-line', cited && 'is-cited')}>
                  <span className="ln">{lineNo}</span>
                  <span className="code-text" dangerouslySetInnerHTML={{ __html: line || ' ' }} />
                </span>
              );
            })}
          </pre>
        )}
      </div>
    </>
  );
}
