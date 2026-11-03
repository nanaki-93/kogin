// All tasks, grouped by what they need, with keyboard navigation (j/k, Enter).

import { useMemo, useState } from 'react';
import { GitBranch, ListTodo, Plus, Search, Trash2 } from 'lucide-react';
import { useTasks } from '@/api/hooks';
import type { TaskEntry } from '@/api/types';
import { openNewTask } from '@/app/store';
import { useActions, useBusy, type DeletableTask } from '@/app/actions';
import { usePageKeys } from '@/app/shortcuts';
import { navigate, setParam, useRoute } from '@/lib/router';
import { rankItems } from '@/lib/fuzzy';
import { taskStatus, type TaskStatus } from '@/lib/readiness';
import { basename, plural, timeAgo } from '@/lib/util';
import { Button, EmptyState, Highlight, Input, ListGroup, Segmented, Skeleton, cx } from '@/components/ui';
import { KindTag, StatusGlyph } from '@/components/domain';

const GROUPS: { id: string; label: string; statuses: TaskStatus[] }[] = [
  { id: 'ready', label: 'Ready to accept', statuses: ['ready'] },
  { id: 'needs', label: 'Needs you', statuses: ['needs_you'] },
  { id: 'running', label: 'Agents working', statuses: ['running'] },
  { id: 'attention', label: 'Failed or stale', statuses: ['failed', 'stale'] },
  { id: 'draft', label: 'Drafts', statuses: ['draft', 'canceled'] },
  { id: 'done', label: 'Done', statuses: ['applied', 'done'] },
];

type Filter = 'active' | 'running' | 'done' | 'all';

export function TaskListPage() {
  const route = useRoute();
  const { tasks, isLoading } = useTasks();
  const filter = (route.params.get('filter') as Filter) || 'active';
  const [q, setQ] = useState('');
  const [cursor, setCursor] = useState(0);
  const actions = useActions();
  const busy = useBusy();

  const counts = useMemo(() => {
    const result: Record<Filter, number> = { active: 0, running: 0, done: 0, all: tasks.length };
    for (const task of tasks) for (const name of ['active', 'running', 'done'] as const) if (inFilter(task, name)) result[name]++;
    return result;
  }, [tasks]);
  const visible = useMemo(() => rankItems(tasks.filter(task => inFilter(task, filter)), q, task => `${task.title} ${task.kind} ${task.id}`, 500), [tasks, filter, q]);

  const grouped = useMemo(
    () =>
      GROUPS.map(group => ({ ...group, items: visible.filter(v => group.statuses.includes(taskStatus(v.item).status)) })).filter(group => group.items.length),
    [visible],
  );
  const flat = grouped.flatMap(group => group.items.map(v => v.item));

  usePageKeys(key => {
    if (key === 'j' || key === 'ArrowDown') setCursor(c => Math.min(c + 1, flat.length - 1));
    else if (key === 'k' || key === 'ArrowUp') setCursor(c => Math.max(c - 1, 0));
    else if (key === 'Enter' && flat[cursor]) openTask(flat[cursor]);
    else if ((key === 'Delete' || key === 'Backspace') && flat[cursor] && taskStatus(flat[cursor]).status !== 'running') void actions.deleteTasks([deletable(flat[cursor])]);
    else return false;
    return true;
  });

  return (
    <div className="page-scroll">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">Tasks</h1>
          <p className="page-sub">Each task is a bounded change that agents write, test and review. Nothing reaches your files until you apply it.</p>
        </div>
      </div>
      <div className="toolbar">
        <Segmented
          label="Show tasks"
          value={filter}
          onChange={value => setParam('filter', value === 'active' ? undefined : value)}
          options={[
            { value: 'active', label: 'Active', count: counts.active },
            { value: 'running', label: 'Running', count: counts.running },
            { value: 'done', label: 'Done', count: counts.done },
            { value: 'all', label: 'All', count: counts.all },
          ]}
        />
        <span className="spacer" />
        <Input icon={Search} placeholder="Filter tasks…" value={q} onChange={e => setQ(e.target.value)} aria-label="Filter tasks" className="toolbar-search" />
      </div>
      {isLoading ? (
        <Skeleton lines={6} className="list-skeleton" />
      ) : !flat.length ? (
        <TasksEmpty total={tasks.length} filter={filter} query={q} />
      ) : (
        <div className="list">
          {grouped.map(group => (
            <ListGroup
              key={group.id}
              title={group.label}
              count={group.items.length}
              action={group.id === 'attention' || group.id === 'draft' ? (
                <Button size="sm" variant="ghost" icon={Trash2} onClick={() => void actions.deleteTasks(group.items.map(v => deletable(v.item)))} disabled={Boolean(busy)}>
                  {group.items.length === 1 ? 'Delete' : `Delete all ${group.items.length}`}
                </Button>
              ) : undefined}
            >
              {group.items.map(({ item, positions }) => {
                const index = flat.indexOf(item);
                return <TaskRow key={item.id} task={item} positions={positions} active={index === cursor} onHover={() => setCursor(index)} />;
              })}
            </ListGroup>
          ))}
        </div>
      )}
    </div>
  );
}

export function deletable(task: TaskEntry): DeletableTask {
  return { id: task.id, title: task.title, worktreePath: task.worktreePath, project_id: task.project_id, project_revision: task.project_revision, revision: task.revision, hash: task.hash };
}

export function openTask(task: TaskEntry) {
  navigate(`/tasks/${task.id}`, { wt: task.worktreePath || undefined });
}

export function TaskRow({ task, positions = [], active, onHover, compact }: { task: TaskEntry; positions?: number[]; active?: boolean; onHover?: () => void; compact?: boolean }) {
  const status = taskStatus(task);
  return (
    <a
      href={`#/tasks/${encodeURIComponent(task.id)}${task.worktreePath ? `?wt=${encodeURIComponent(task.worktreePath)}` : ''}`}
      className={cx('list-row', active && 'is-active')}
      role="listitem"
      onMouseEnter={onHover}
      onClick={event => {
        event.preventDefault();
        openTask(task);
      }}
    >
      <StatusGlyph status={status.status} />
      <span className="list-title truncate">
        <Highlight text={task.title || task.id} positions={positions.filter(p => p < (task.title || '').length)} />
      </span>
      {!compact ? <RowDetail task={task} detail={status.detail} /> : null}
      <span className="spacer" />
      {task.worktreePath ? (
        <span className="chip" title={task.worktreePath}>
          <GitBranch size={12} aria-hidden />
          {task.branch || basename(task.worktreePath)}
        </span>
      ) : null}
      <span className={cx('col col-status status-text', `tone-${status.tone}`)}>{status.label}</span>
      <span className="col col-kind">{compact ? null : <KindTag kind={task.kind} />}</span>
      <span className="col col-files xs faint">{!compact && task.paths?.length ? plural(task.paths.length, 'file') : ''}</span>
      <span className="col col-time xs faint">{timeAgo(task.updated_at)}</span>
    </a>
  );
}

/** Why a run ended (failed, canceled, out of date, paused), else the status's own line, such as files viewed. */
function RowDetail({ task, detail }: { task: TaskEntry; detail?: string }) {
  const status = taskStatus(task).status;
  const reason = task.workflow_reason && (status === 'failed' || status === 'canceled' || task.workflow_status === 'stale' || task.workflow_status === 'paused') ? task.workflow_reason : '';
  const text = reason || detail;
  return text ? <span className="list-reason truncate" title={text}>{text}</span> : null;
}

/** Whether a task shows under a filter. */
function inFilter(task: TaskEntry, filter: Filter): boolean {
  const status = taskStatus(task).status;
  if (filter === 'running') return status === 'running';
  if (filter === 'done') return status === 'applied' || status === 'done';
  if (filter === 'active') return status !== 'applied' && status !== 'done';
  return true;
}

const EMPTY_FILTER: Record<Filter, string> = { active: 'Nothing in progress', running: 'No agents running', done: 'No applied tasks yet', all: 'No tasks yet' };

/** Says why the list is empty and offers the way on: a new task, or every task. */
function TasksEmpty({ total, filter, query }: { total: number; filter: Filter; query: string }) {
  const newTask = <Button variant="primary" icon={Plus} onClick={() => openNewTask()}>New task</Button>;
  if (!total) {
    return <EmptyState icon={ListTodo} title="No tasks yet" action={newTask}>Describe a bug to fix or a feature to build. Agents write the change and tests; you review and apply it.</EmptyState>;
  }
  if (query.trim()) return <EmptyState icon={ListTodo} title="No tasks match">Nothing here matches “{query.trim()}”. Try other words or another filter.</EmptyState>;
  return (
    <EmptyState icon={ListTodo} title={EMPTY_FILTER[filter]} action={<div className="row">{newTask}<Button variant="ghost" onClick={() => setParam('filter', 'all')}>Show all {total}</Button></div>}>
      Every task you have is under another filter.
    </EmptyState>
  );
}
