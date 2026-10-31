// The project checks of an analysis run: the commands its confirmation names,
// and how each check went once the run's files were done.

import { ListChecks } from 'lucide-react';
import type { AnalysisChecks, AnalysisChecksPlan } from '@/api/types';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge } from '@/components/ui';
import { statusLabel, statusTone } from '@/lib/analysis';

/** The commands a run will execute, for its confirmation. */
export function ChecksPlanNote({ plan }: { plan?: AnalysisChecksPlan }) {
  if (!plan) return null;
  const commands = plan.commands ?? [];
  return (
    <div className="stack-tight checks-plan">
      <p>
        Then checks the parser diagnostics and the AGENTS.md code budgets of every file
        {commands.length ? ', and runs these commands in a copy of the project:' : '.'}
      </p>
      {commands.length ? (
        <ul className="checks-commands">
          {commands.map(command => (
            <li key={command.name}>
              <span className="small">{command.name}</span> <code className="mono xs">{command.command.join(' ')}</code>
            </li>
          ))}
        </ul>
      ) : null}
      {plan.reason ? <p className="xs muted">{plan.reason}</p> : null}
    </div>
  );
}

const CHECK_TONES: Record<string, 'success' | 'danger' | 'warning' | 'neutral'> = { passed: 'success', failed: 'danger', skipped: 'neutral', canceled: 'warning', unavailable: 'warning' };

/** How the run's checks went; failures are issues on the Issues page. */
export function RunChecks({ checks }: { checks?: AnalysisChecks | null }) {
  if (!checks) return null;
  return (
    <section className="card" aria-label="Checks">
      <div className="card-head">
        <ListChecks size={15} aria-hidden />
        <span className="strong">Checks</span>
        <Badge tone={statusTone(checks.status)}>{statusLabel(checks.status)}</Badge>
        <span className="spacer" />
        {checks.issues ? (
          <button type="button" className="link-btn small" onClick={() => navigate('/issues')}>{plural(checks.issues, 'issue')}</button>
        ) : (
          <span className="xs muted">No issues</span>
        )}
      </div>
      {checks.reason ? <div className="card-body xs muted">{checks.reason}</div> : null}
      <ul className="model-list">
        {checks.checks.map(check => (
          <li key={check.name} className="row">
            <span className="small">{check.name}</span>
            {check.command?.length ? <code className="mono xs muted truncate">{check.command.join(' ')}</code> : null}
            <span className="spacer" />
            {check.issues ? <span className="xs muted">{plural(check.issues, 'issue')}</span> : null}
            <Badge tone={CHECK_TONES[check.state] ?? 'neutral'}>{check.state}</Badge>
          </li>
        ))}
      </ul>
    </section>
  );
}
