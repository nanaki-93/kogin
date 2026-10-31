// Domain-specific display pieces shared by several pages.

import { useState } from 'react';
import { Bug, FileCode2, FileJson, FileText, FlaskConical, Gauge, Lightbulb, Shield, Sparkles, type LucideIcon } from 'lucide-react';
import type { ChangeHistoryEntry, Finding } from '@/api/types';
import { taskStatus, type TaskStatus } from '@/lib/readiness';
import { commentsBrief, isTestPath, plural, severityTone } from '@/lib/util';
import type { RunStart, RunStartOption } from '@/lib/workflow';
import { Badge, cx } from './ui';

export const KINDS: { value: string; label: string; icon: LucideIcon }[] = [
  { value: 'fix', label: 'Fix', icon: Bug },
  { value: 'feature', label: 'Feature', icon: Lightbulb },
  { value: 'performance', label: 'Performance', icon: Gauge },
  { value: 'security', label: 'Security', icon: Shield },
];

export function kindInfo(kind: string) {
  return KINDS.find(k => k.value === kind) ?? { value: kind, label: kind === 'instructions' ? 'Instructions' : kind, icon: Sparkles };
}

export function KindTag({ kind }: { kind: string }) {
  const info = kindInfo(kind);
  return (
    <span className="kind-tag">
      <info.icon size={12} aria-hidden />
      {info.label}
    </span>
  );
}

const STATUS_GLYPH: Record<TaskStatus, string> = {
  draft: 'draft', running: 'running', needs_you: 'review', ready: 'ready', applied: 'done', failed: 'failed', stale: 'stale', canceled: 'canceled', done: 'done',
};

/** A Linear-style status glyph: circle states drawn in CSS. */
export function StatusGlyph({ status, size = 14 }: { status: TaskStatus; size?: number }) {
  return <span className={cx('glyph', `glyph-${STATUS_GLYPH[status]}`)} style={{ width: size, height: size }} aria-hidden />;
}

export function TaskStatusBadge({ entry }: { entry: Pick<ChangeHistoryEntry, 'state' | 'freshness' | 'workflow_status' | 'commit' | 'revision' | 'hash'> }) {
  const info = taskStatus(entry);
  return (
    <span className={cx('status-pill', `status-${info.tone}`)}>
      <StatusGlyph status={info.status} size={12} />
      {info.label}
    </span>
  );
}

export function SeverityBadge({ severity }: { severity: string }) {
  const tone = severityTone(severity);
  return <Badge tone={tone}>{severity || 'info'}</Badge>;
}

const SOURCE_LABELS: Record<string, string> = {
  ai: 'AI suggestion',
  vet: 'Verified by go vet',
  parser: 'Verified by parser',
  test: 'Failing test',
  lint: 'Lint',
  semgrep: 'Semgrep rule',
  security_rules: 'Security rule',
  rules: 'Code budget (AGENTS.md)',
};

/** Where a finding comes from; model output is never presented as verified. */
export function sourceLabel(finding: Finding): string {
  const source = (finding.source || '').toLowerCase();
  return SOURCE_LABELS[source] ?? (source ? source.replace(/_/g, ' ').replace(/^./, c => c.toUpperCase()) : 'Finding');
}

/** An issue still in the code: open, or being fixed by a task not merged yet. */
export function isUnresolvedFinding(finding: Finding): boolean {
  return !['dismissed', 'fixed', 'retired'].includes(finding.status || 'open');
}

/** An issue a task is fixing: out of the open issues until the task is merged, deleted or discarded. */
export function isInProgressFinding(finding: Finding): boolean {
  return finding.status === 'in_progress';
}

/** An issue no task is fixing yet. */
export function isOpenFinding(finding: Finding): boolean {
  return isUnresolvedFinding(finding) && !isInProgressFinding(finding);
}

export function FileIcon({ path, size = 14 }: { path: string; size?: number }) {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
  const Icon = isTestPath(path) ? FlaskConical : ext === 'json' || ext === 'yaml' || ext === 'yml' || ext === 'toml' ? FileJson : ext === 'md' || ext === 'txt' ? FileText : FileCode2;
  return <Icon size={size} className={cx('file-icon', `file-${ext}`, isTestPath(path) && 'file-test')} aria-hidden />;
}

/**
 * Where a run of the agents starts; a start the daemon would refuse says why.
 * Open line comments go with a start that writes code and stay open otherwise.
 */
export function RunStartChoice({ options, initial, comments = 0, onChange }: { options: RunStartOption[]; initial: RunStart; comments?: number; onChange: (start: RunStart) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <fieldset className="choice-list">
        <legend className="small strong">Start from</legend>
        {options.map(option => (
          <label key={option.value} className={cx('choice', value === option.value && 'is-selected', option.blocked && 'is-disabled')}>
            <input
              type="radio"
              name="run-start"
              value={option.value}
              checked={value === option.value}
              disabled={Boolean(option.blocked)}
              onChange={() => {
                setValue(option.value);
                onChange(option.value);
              }}
            />
            <span className="choice-text">
              <span className="small strong">{option.label}</span>
              <span className="xs muted">{option.blocked || option.detail}</span>
            </span>
          </label>
        ))}
      </fieldset>
      {comments ? <p className="small">{value === 'checks' ? `Your ${plural(comments, 'line comment')} ${comments === 1 ? 'stays' : 'stay'} open: this start writes no code.` : commentsBrief(comments)}</p> : null}
    </>
  );
}
