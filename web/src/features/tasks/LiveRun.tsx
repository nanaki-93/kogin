// While a task's agents run: what the agent does now, the tool calls it made
// (reads, searches, edits, checks), the files it changed so far, time and
// spend, and Stop. The calls come from Kogin's own tools as the agent makes
// them; a stage without tools (a reviewer) shows only its time.

import { useEffect, useMemo, useState } from 'react';
import { CircleAlert, CircleCheck, FilePen, LoaderCircle, Square } from 'lucide-react';
import type { AgentActivity, LoadedSession } from '@/api/types';
import { useActivity } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { workflowStages } from '@/lib/workflow';
import { formatElapsed, plural } from '@/lib/util';
import { costLabel, formatTokens } from '@/lib/usage';
import { Button, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';

const VERBS: Record<string, string> = {
  read_file: 'Read', list_files: 'Listed files', search: 'Searched', outline: 'Outlined', find_definitions: 'Looked up', find_references: 'Found uses of',
  edit_file: 'Edited', create_file: 'Created', delete_file: 'Deleted', rename_file: 'Renamed', run_checks: 'Ran the checks', widen_scope: 'Widened the scope to', finish: 'Finished',
};
const NOW: Record<string, string> = {
  read_file: 'is reading', list_files: 'is listing files', search: 'is searching', outline: 'is outlining', find_definitions: 'is looking up', find_references: 'is finding uses of',
  edit_file: 'is editing', create_file: 'is creating', delete_file: 'is deleting', rename_file: 'is renaming', run_checks: 'is running the checks', widen_scope: 'is widening the scope to', finish: 'is finishing',
};
const ROLE: Record<string, string> = { coder: 'Creation agent', tester: 'Testing agent' };
const WRITES = new Set(['edit_file', 'create_file', 'delete_file', 'rename_file']);

export function LiveRun({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const workflow = session.workflow!;
  const activity = useActivity(session, true).data;
  const entries = useMemo(() => (activity && (!activity.workflow_id || activity.workflow_id === workflow.id) ? activity.entries : []), [activity, workflow.id]);
  const stage = workflowStages(workflow).find(item => item.status === 'running') ?? null;
  const now = useNow();
  const latest = entries.at(-1);
  const touched = useMemo(() => [...new Set(entries.filter(entry => WRITES.has(entry.tool) && entry.status === 'done' && entry.target).map(entry => entry.target!))], [entries]);
  const start = Date.parse(entries[0]?.at ?? stage?.startedAt ?? '');
  const usage = stage?.usage;
  const cost = costLabel(stage?.cost);
  const canceling = workflow.status === 'canceling';

  return (
    <section className="live-run" aria-label="Agents at work">
      <div className="live-run-head">
        <span className={cx('live-pulse', canceling && 'is-stopping')} aria-hidden />
        <span className="strong">{canceling ? 'Stopping the agents…' : headline(stage?.label, latest)}</span>
        {latest?.target && latest.status === 'running' && !canceling ? <span className="mono small live-target truncate" title={latest.target}>{latest.target}</span> : null}
        <span className="spacer" />
        <span className="live-meter">
          {Number.isFinite(start) ? <span title="Time since the first call">{formatElapsed(now - start)}</span> : null}
          {entries.length ? <span>{plural(entries.length, 'tool call')}</span> : null}
          {usage?.input_tokens ? <span>{formatTokens(usage.input_tokens + (usage.output_tokens ?? 0))} tokens{cost ? ` · ${cost}` : ''}</span> : null}
        </span>
        <Button size="sm" variant="secondary" icon={Square} onClick={() => void actions.cancelAgents(session)} disabled={canceling || Boolean(busy)}>Stop</Button>
      </div>
      {entries.length ? (
        <div className="live-run-body">
          <ol className="live-steps" aria-label="Tool calls">
            {entries.slice(-12).map(entry => (
              <li key={entry.seq} className={cx('live-step', `is-${entry.status}`)}>
                <span className="live-time">{Number.isFinite(start) ? formatElapsed(Date.parse(entry.at) - start) : ''}</span>
                <StepIcon status={entry.status} />
                <span className="live-what">
                  {ROLE[entry.role] && entry.role !== entries[0]?.role ? <span className="faint">{ROLE[entry.role]} · </span> : null}
                  {VERBS[entry.tool] ?? entry.tool}
                  {entry.target ? <code title={entry.target}>{entry.target}</code> : null}
                  {entry.detail ? <span className="live-detail tone-danger">{entry.detail}</span> : null}
                </span>
              </li>
            ))}
          </ol>
          <div className="live-files">
            <div className="xs muted">{touched.length ? `Changed so far · ${plural(touched.length, 'file')}` : 'No file changed yet'}</div>
            {touched.map(path => (
              <div key={path} className="live-file small" title={path}><FileIcon path={path} size={13} /><span className="truncate">{path}</span></div>
            ))}
          </div>
        </div>
      ) : (
        <p className="small muted live-empty">{stage ? `${stage.label} · ${stage.actor}` : 'Starting…'}. Tool calls show here as the agent makes them.</p>
      )}
    </section>
  );
}

function headline(stage: string | undefined, latest: AgentActivity | undefined): string {
  if (!latest) return stage ? `${stage}…` : 'Agents are starting…';
  const who = ROLE[latest.role] ?? 'The agent';
  if (latest.status === 'running') return `${who} ${NOW[latest.tool] ?? `is calling ${latest.tool}`}`;
  return `${who} is thinking`;
}

function StepIcon({ status }: { status: string }) {
  if (status === 'running') return <LoaderCircle size={13} className="spin live-icon" aria-label="Running" />;
  if (status === 'failed') return <CircleAlert size={13} className="tone-danger live-icon" aria-label="Failed" />;
  if (status === 'done') return <CircleCheck size={13} className="tone-success live-icon" aria-label="Done" />;
  return <FilePen size={13} className="live-icon" aria-hidden />;
}

/** The time, every second, for the running clock. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}
