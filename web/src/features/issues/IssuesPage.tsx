// Issues from analysis and scans: a filterable table with a detail pane and
// one-click "fix with agents".

import { useMemo, useState } from 'react';
import { usePageKeys } from '@/app/shortcuts';
import { CircleAlert, ExternalLink, Search, Wrench, XCircle, RotateCcw } from 'lucide-react';
import { useFindings, useIndex, useProject } from '@/api/hooks';
import type { Finding } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { navigate, setParam, useRoute } from '@/lib/router';
import { rankItems } from '@/lib/fuzzy';
import { severityRank } from '@/lib/util';
import { Badge, Button, EmptyState, Input, Segmented, Skeleton, cx } from '@/components/ui';
import { SeverityBadge, isOpenFinding, sourceLabel } from '@/components/domain';

type StatusFilter = 'open' | 'dismissed' | 'all';

export function IssuesPage() {
  const route = useRoute();
  const { data, isLoading } = useFindings();
  const all = data?.findings ?? [];
  const status = (route.params.get('status') as StatusFilter) || 'open';
  const severity = route.params.get('severity') ?? 'all';
  const category = route.params.get('category') ?? '';
  const selectedId = route.params.get('id') ?? '';
  const [q, setQ] = useState('');

  const list = useMemo(() => {
    const filtered = all.filter(f => (status === 'all' ? true : status === 'open' ? isOpenFinding(f) : !isOpenFinding(f))).filter(f => severity === 'all' || (f.severity || '').toLowerCase() === severity).filter(f => !category || f.category === category);
    const sorted = [...filtered].sort((a, b) => severityRank(a.severity) - severityRank(b.severity) || (a.location?.path ?? '').localeCompare(b.location?.path ?? ''));
    return rankItems(sorted, q, f => `${f.title} ${f.location?.path ?? ''} ${f.category ?? ''}`, 1000).map(r => r.item);
  }, [all, status, severity, category, q]);
  const selected = list.find(f => f.id === selectedId) ?? null;
  const categoryCounts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const f of all.filter(isOpenFinding)) if (f.category) c[f.category] = (c[f.category] ?? 0) + 1;
    return c;
  }, [all]);
  const categories = Object.keys(categoryCounts).sort();
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = useBusy();
  const index = list.findIndex(f => f.id === selectedId);

  // j/k move through the list, Esc closes the detail, f fixes and x dismisses or reopens.
  usePageKeys(key => {
    if (key === 'j' || key === 'ArrowDown') select(list[Math.min(index + 1, list.length - 1)]);
    else if (key === 'k' || key === 'ArrowUp') select(list[Math.max(index - 1, 0)]);
    else if (key === 'Escape' && selected) setParam('id', undefined);
    else if (key === 'f' && selected && isOpenFinding(selected)) void actions.fixIssue(project, worktreePath, selected);
    else if (key === 'x' && selected && project && !busy) void actions.triageFinding(project, worktreePath, selected, isOpenFinding(selected) ? 'dismissed' : 'open');
    else return false;
    return true;
  });
  function select(finding: Finding | undefined) {
    if (!finding) return;
    setParam('id', finding.id);
    document.getElementById(`issue-${finding.id}`)?.scrollIntoView({ block: 'nearest' });
  }

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const f of all.filter(isOpenFinding)) c[(f.severity || 'info').toLowerCase()] = (c[(f.severity || 'info').toLowerCase()] ?? 0) + 1;
    return c;
  }, [all]);

  return (
    <div className="split-page">
      <div className="split-main page-scroll">
        <div className="page-head">
          <div className="grow">
            <h1 className="page-title">Issues</h1>
            <p className="page-sub">Findings from analysis and scans. AI suggestions are hypotheses until a test reproduces them.</p>
          </div>
        </div>
        <div className="toolbar">
          <Segmented label="Status" value={status} onChange={v => setParam('status', v === 'open' ? undefined : v)} options={[{ value: 'open', label: 'Open' }, { value: 'dismissed', label: 'Dismissed' }, { value: 'all', label: 'All' }]} />
          <div className="chips">
            {['all', 'critical', 'high', 'medium', 'low'].map(s => (
              <button key={s} type="button" className={cx('chip', 'chip-toggle', severity === s && 'is-on')} onClick={() => setParam('severity', s === 'all' ? undefined : s)}>
                {s === 'all' ? 'All severities' : s}
                {s !== 'all' && counts[s] ? <span className="count">{counts[s]}</span> : null}
              </button>
            ))}
          </div>
          {categories.length > 1 || category ? (
            <div className="chips" role="group" aria-label="Category">
              {['', ...categories].map(c => (
                <button key={c || 'all'} type="button" className={cx('chip', 'chip-toggle', category === c && 'is-on')} onClick={() => setParam('category', c || undefined)}>
                  {c ? CATEGORY_LABELS[c] ?? c : 'All kinds'}
                  {c && categoryCounts[c] ? <span className="count">{categoryCounts[c]}</span> : null}
                </button>
              ))}
            </div>
          ) : null}
          <span className="spacer" />
          <Input icon={Search} placeholder="Filter issues…" value={q} onChange={e => setQ(e.target.value)} aria-label="Filter issues" className="toolbar-search" />
        </div>
        {isLoading ? (
          <Skeleton lines={8} />
        ) : !list.length ? (
          <EmptyState icon={CircleAlert} title={all.length ? 'No issues match' : 'No issues yet'}>
            {all.length ? 'Try another filter.' : 'Run a project analysis to find bugs, performance and security issues.'}
          </EmptyState>
        ) : (
          <table className="table" aria-label="Issues">
            <thead>
              <tr>
                <th className="col-sev">Severity</th>
                <th>Issue</th>
                <th className="col-loc">Location</th>
                <th className="col-src">Source</th>
              </tr>
            </thead>
            <tbody>
              {list.map(f => (
                <tr key={f.id} id={`issue-${f.id}`} className={cx('table-row', f.id === selectedId && 'is-active', !isOpenFinding(f) && 'is-muted')} onClick={() => setParam('id', f.id)} tabIndex={0} onKeyDown={e => e.key === 'Enter' && setParam('id', f.id)} aria-selected={f.id === selectedId}>
                  <td><SeverityBadge severity={f.severity} /></td>
                  <td>
                    <div className="truncate strong-row">{f.title}</div>
                    {f.category ? <div className="xs faint">{f.category}</div> : null}
                  </td>
                  <td className="mono xs truncate" title={[f.location?.path, f.location?.symbol].filter(Boolean).join(' · ')}>{f.location?.path}{f.location?.start_line ? `:${f.location.start_line}` : ''}{f.location?.symbol ? <span className="faint"> · {f.location.symbol}</span> : null}</td>
                  <td className="xs muted">{sourceLabel(f)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {selected ? <IssueDetail finding={selected} onClose={() => setParam('id', undefined)} /> : null}
    </div>
  );
}

const CATEGORY_LABELS: Record<string, string> = { bugs: 'Bugs', bug: 'Bugs', performance: 'Performance', security: 'Security', maintainability: 'Maintainability' };

function IssueDetail({ finding, onClose }: { finding: Finding; onClose: () => void }) {
  const project = useProject().data;
  const files = useIndex().data?.files ?? [];
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = useBusy();
  const open = isOpenFinding(finding);
  const path = finding.location?.path || finding.task_spec?.target_path || '';

  return (
    <aside className="detail-pane" aria-label="Issue detail">
      <div className="detail-head">
        <SeverityBadge severity={finding.severity} />
        <Badge tone={sourceLabel(finding) === 'AI suggestion' ? 'accent' : 'neutral'}>{sourceLabel(finding)}</Badge>
        <span className="spacer" />
        <button type="button" className="icon-btn" aria-label="Close detail" onClick={onClose}>
          <XCircle size={16} aria-hidden />
        </button>
      </div>
      <h2 className="detail-title">{finding.title}</h2>
      {path ? (
        <button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: path, line: finding.location?.start_line ? String(finding.location.start_line) : undefined })}>
          {path}{finding.location?.start_line ? `:${finding.location.start_line}` : ''} <ExternalLink size={11} aria-hidden />
        </button>
      ) : null}
      {finding.location?.symbol ? <div className="mono xs faint">in {finding.location.symbol}</div> : null}
      <p className="prose small">{finding.message}</p>
      {finding.evidence ? (
        <div>
          <div className="label-row">Evidence</div>
          <pre className="output">{finding.evidence}</pre>
        </div>
      ) : null}
      {finding.task_spec?.acceptance_criteria?.length ? (
        <div>
          <div className="label-row">A fix is done when</div>
          <ul className="small">{finding.task_spec.acceptance_criteria.map((c, i) => <li key={i}>{c}</li>)}</ul>
        </div>
      ) : null}
      <div className="row-wrap detail-actions">
        {open ? <Button variant="primary" icon={Wrench} onClick={() => void actions.fixIssue(project, worktreePath, finding)} disabled={!files.length} kbd="F">Fix with agents</Button> : null}
        {project ? (
          open ? (
            <Button variant="ghost" icon={XCircle} onClick={() => void actions.triageFinding(project, worktreePath, finding, 'dismissed')} disabled={Boolean(busy)} kbd="X">Dismiss</Button>
          ) : (
            <Button variant="ghost" icon={RotateCcw} onClick={() => void actions.triageFinding(project, worktreePath, finding, 'open')} disabled={Boolean(busy)} kbd="X">Reopen</Button>
          )
        ) : null}
      </div>
    </aside>
  );
}
