// How the task's files stand against the budgets and import rules of their
// AGENTS.md files, measured live on the proposal the task holds now.

import { Ruler, TriangleAlert } from 'lucide-react';
import type { ChangeRulesFile, LoadedSession } from '@/api/types';
import { useChangeRules } from '@/api/hooks';
import { describeBudgetFinding, describeBudgets } from '@/lib/rules';
import { unique } from '@/lib/util';
import { Badge, Callout, cx } from '@/components/ui';
import { FileIcon } from '@/components/domain';

export function CodeRules({ session }: { session: LoadedSession }) {
  const { data } = useChangeRules(session);
  const files = (data?.files ?? []).filter(file => describeBudgets(file.budgets) || file.imports?.length);
  if (!data || (!files.length && !data.problems.length)) return null;
  const sources = unique(files.flatMap(file => file.sources ?? []));
  const hasAttention = data.problems.length > 0 || files.some(file => Boolean(file.over?.length || file.debt?.length));
  return (
    <section className={cx('card', !hasAttention && 'card-quiet')} aria-label="Code rules">
      <details className="card-fold" open={hasAttention || undefined}>
        <summary className="card-head">
          <Ruler size={15} className={hasAttention ? undefined : 'tone-success'} aria-hidden />
          <span className="strong">Code rules</span>
          {!hasAttention && files.length ? (
            <Badge tone="success">All {files.length === 1 ? '1 file' : `${files.length} files`} within budget</Badge>
          ) : null}
          {sources.length ? <span className="xs faint">from {sources.join(', ')}</span> : null}
        </summary>
        <div className="card-body stack">
          {data.problems.map(problem => (
            <Callout key={problem.path} tone="warning" icon={TriangleAlert} title={`The rules block in ${problem.path} is not applied`}>
              {problem.message}
            </Callout>
          ))}
          <ul className="rules-list">
            {files.map(file => (
              <RulesFile key={file.path} file={file} />
            ))}
          </ul>
        </div>
      </details>
    </section>
  );
}

function RulesFile({ file }: { file: ChangeRulesFile }) {
  const status = fileStatus(file);
  const longer = Boolean(file.budgets.file_lines && file.lines > file.budgets.file_lines);
  return (
    <li className="rules-file">
      <div className="rules-file-head">
        <FileIcon path={file.path} size={13} />
        <span className="mono small grow">{file.path}</span>
        {file.budgets.file_lines ? (
          <span className={cx('small rules-lines', longer && 'rules-lines-over')} title={file.base_lines && file.proposed ? `${file.base_lines} lines before this task` : undefined}>
            {file.lines} / {file.budgets.file_lines} lines
          </span>
        ) : null}
        <Badge tone={status.tone}>{status.label}</Badge>
      </div>
      <div className="xs muted">
        {describeBudgets(file.budgets) || 'No budgets'}
        {file.imports?.length ? ` · ${file.imports.length} import ${file.imports.length === 1 ? 'rule' : 'rules'}` : ''}
      </div>
      {file.over?.length ? (
        <ul className="rules-findings rules-findings-over">
          {file.over.map(finding => (
            <li key={`${finding.kind}-${finding.function ?? ''}-${finding.line ?? 0}`}>{describeBudgetFinding(finding)}</li>
          ))}
        </ul>
      ) : null}
      {file.debt?.length ? (
        <div className="xs">
          <span className="muted">Already over before this task; it may not grow (tracked as an issue):</span>
          <ul className="rules-findings">
            {file.debt.map(finding => (
              <li key={`${finding.kind}-${finding.function ?? ''}-${finding.line ?? 0}`}>{describeBudgetFinding(finding)}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {file.not_checked ? <div className="xs faint">Functions not measured: {file.not_checked}. File lines still count.</div> : null}
    </li>
  );
}

function fileStatus(file: ChangeRulesFile): { tone: 'danger' | 'warning' | 'neutral' | 'success'; label: string } {
  if (file.over?.length) return { tone: 'danger', label: 'Over budget' };
  if (file.debt?.length) return { tone: 'warning', label: 'Already over' };
  if (file.not_checked) return { tone: 'neutral', label: 'Lines only' };
  return { tone: 'success', label: 'Within budget' };
}
