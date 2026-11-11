// Checks for one proposal: what ran, what it proved, and its output.

import { useState } from 'react';
import { ChevronRight, FlaskConical, Play, ShieldOff } from 'lucide-react';
import type { Check, LoadedSession } from '@/api/types';
import { useTrust } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { navigate } from '@/lib/router';
import { checkSummary, phaseTone, proposalReadiness } from '@/lib/readiness';
import { formatDuration } from '@/lib/util';
import { Badge, Button, Callout, EmptyState, PhaseIcon, cx } from '@/components/ui';

const OUTCOME_LABEL: Record<string, string> = {
  failed_before_change: 'Reproduced: failed before the change',
  already_passes: 'Already passed before the change',
  could_not_execute: 'Could not run',
  known_failures: 'Only failures from before the change',
  also_failed_before: 'Also failed before the change',
  not_affected: 'Not affected',
};

export function ChecksPanel({ session }: { session: LoadedSession }) {
  const actions = useActions();
  const busy = useBusy();
  const trust = useTrust().data;
  const ready = proposalReadiness(session);
  const checks = session.checks ?? [];
  const summary = checkSummary(checks, session.checks_outdated);
  const canRun = ready.editable && (session.changes ?? []).length > 0;

  return (
    <div className="page-inner stack-lg">
      <div className="row">
        <div className="grow">
          <h2 className="h2">Checks</h2>
          <p className="muted small">Source checks always run. Tests and lint run in an isolated copy of the project and need this project to be trusted.</p>
        </div>
        <Button variant="primary" icon={Play} onClick={() => void actions.runChecks(session)} disabled={!canRun || !trust?.trusted || Boolean(busy)} loading={busy === 'Running checks…'}>
          {checks.length ? 'Run again' : 'Run checks'}
        </Button>
      </div>
      {!trust?.trusted ? (
        <Callout tone="warning" icon={ShieldOff} title="Checks need your permission to run project code" action={<Button size="sm" onClick={() => navigate('/settings/trust')}>Review trust</Button>}>
          Without trust only source checks (parsing, formatting, boundaries) run.
        </Callout>
      ) : null}
      {checks.length && session.checks_outdated ? (
        <Callout tone="warning" icon={FlaskConical} title="These checks ran on an earlier version of the project">
          Files outside this task changed since. The task is still current; run the checks again before approving.
        </Callout>
      ) : null}
      {!checks.length ? (
        <EmptyState icon={FlaskConical} title="No checks yet">
          Checks run automatically during the agent workflow, or on demand here.
        </EmptyState>
      ) : (
        <div className="card">
          <div className="card-head">
            <span className={cx('strong', `tone-${summary.tone}`)}>{summary.label}</span>
            <span className="muted small">
              {summary.passed} passed · {summary.failed} failed{summary.skipped ? ` · ${summary.skipped} skipped` : ''} · {checks.filter(c => c.required).length} required
            </span>
          </div>
          <CheckList checks={checks} />
        </div>
      )}
    </div>
  );
}

/** Checks with failures first, each opening to its command and output. */
export function CheckList({ checks }: { checks: Check[] }) {
  return (
    <ul className="checks">
      {[...checks].sort(order).map(check => (
        <CheckRow key={check.name} check={check} />
      ))}
    </ul>
  );
}

function order(a: Check, b: Check): number {
  const rank = (c: Check) =>
    c.state === 'failed'
      ? 0
      : c.required && c.state !== 'passed' && c.state !== 'running'
        ? 1
        : c.state === 'running'
          ? 2
          : c.state === 'unavailable' || c.state === 'canceled'
            ? 3
            : c.state === 'skipped'
              ? 5
              : 4;
  return rank(a) - rank(b) || Number(b.required) - Number(a.required);
}

function CheckRow({ check }: { check: Check }) {
  const incompleteRequired = check.required && check.state !== 'passed' && check.state !== 'running';
  const [open, setOpen] = useState(check.state === 'failed' || incompleteRequired);
  const rawTone = phaseTone(check.state);
  const tone = incompleteRequired && rawTone === 'idle' ? 'failed' : rawTone;
  const hasDetail = Boolean(check.output || check.command?.length);
  return (
    <li className={cx('check', open && 'is-open')}>
      <button type="button" className="check-head" onClick={() => hasDetail && setOpen(!open)} aria-expanded={hasDetail ? open : undefined} disabled={!hasDetail}>
        <ChevronRight size={14} className={cx('chev', open && 'is-open', !hasDetail && 'is-hidden')} aria-hidden />
        <PhaseIcon tone={tone} />
        <span className="grow truncate">{check.name}</span>
        {check.outcome ? <Badge tone={check.outcome === 'failed_before_change' ? 'success' : check.outcome === 'known_failures' || check.outcome === 'also_failed_before' ? 'warning' : 'neutral'}>{OUTCOME_LABEL[check.outcome] ?? check.outcome}</Badge> : null}
        {check.required ? <Badge>required</Badge> : <Badge tone="neutral">optional</Badge>}
        <span className="xs faint nowrap">{formatDuration(check.duration_ms ?? 0)}</span>
      </button>
      {open && hasDetail ? (
        <div className="check-body">
          {check.command?.length ? <div className="mono xs muted">$ {check.command.join(' ')}</div> : null}
          {check.output ? <pre className="output">{check.output}</pre> : null}
        </div>
      ) : null}
    </li>
  );
}
