// Housekeeping in the Inbox: the reviewed tasks that only keep the tests in
// order, one line each. Those that changed nothing but tests, passed their
// checks, were approved and stayed within what they were asked are accepted
// together in one step; the others say what needs you and open as usual.

import { useQueries } from '@tanstack/react-query';
import { CheckCheck, CircleCheck, Sparkles, TriangleAlert } from 'lucide-react';
import { fetchSession, keys } from '@/api/hooks';
import type { LoadedSession, TaskEntry } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { openTask } from '@/features/tasks/TaskListPage';
import { acceptBlockers, digestLine } from '@/lib/housekeeping';
import { plural } from '@/lib/util';
import { Button, ListGroup } from '@/components/ui';
import { DocText } from '@/features/docs/DocText';

export function Housekeeping({ tasks }: { tasks: TaskEntry[] }) {
  const actions = useActions();
  const busy = useBusy();
  const loaded = useQueries({ queries: tasks.map(task => ({ queryKey: keys.session(task.id), queryFn: () => fetchSession(task.id, task.worktreePath) })) });
  if (!tasks.length) return null;
  const rows = tasks.map((task, index) => {
    const session = loaded[index]?.data as LoadedSession | undefined;
    return { task, session, blockers: session ? acceptBlockers(session) : undefined };
  });
  const ready = rows.filter(row => row.session && row.blockers?.length === 0);
  return (
    <ListGroup
      title="Housekeeping"
      count={tasks.length}
      icon={Sparkles}
      action={ready.length ? (
        <Button size="sm" variant="primary" icon={CheckCheck} onClick={() => void actions.acceptTogether(ready.map(row => row.session!), ready.map(row => digestLine(row.session!)))} disabled={Boolean(busy)}>
          {ready.length === 1 ? 'Accept it' : `Accept all ${ready.length}`}
        </Button>
      ) : undefined}
    >
      {rows.map(({ task, session, blockers }) => (
        <a
          key={task.id}
          href={`#/tasks/${encodeURIComponent(task.id)}`}
          className="list-row housekeeping-row"
          role="listitem"
          onClick={event => {
            event.preventDefault();
            openTask(task);
          }}
        >
          {blockers?.length ? <TriangleAlert size={14} className="tone-warning" aria-hidden /> : <CircleCheck size={14} className={blockers ? 'tone-success' : 'faint'} aria-hidden />}
          <span className="list-main">
            <span className="list-title truncate">{session ? <DocText text={digestLine(session)} shorten /> : task.title}</span>
            <span className="list-reason truncate">
              {!blockers ? 'Loading its review…' : blockers.length ? `Needs you: ${blockers.join('; ')}.` : `Tests only · ${plural((session?.checks ?? []).length, 'check')} passed · reviewers approved · nothing outside what it was asked`}
            </span>
          </span>
          <span className="col col-files xs faint">{task.paths?.length ? plural(task.paths.length, 'file') : ''}</span>
        </a>
      ))}
    </ListGroup>
  );
}
