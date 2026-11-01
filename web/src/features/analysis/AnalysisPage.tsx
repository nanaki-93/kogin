// The analysis dashboard: the last or running analysis with its progress,
// why stages failed, results per section, the models it used and every
// file's stages, with the controls to start, pause, resume, cancel or retry.

import { useMemo, useState } from 'react';
import * as Menu from '@radix-ui/react-dropdown-menu';
import { Activity, AlertTriangle, CheckCircle2, ChevronDown, CircleDashed, Clock, Cpu, FileCode2, Loader2, Pause, Play, RotateCcw, Search, Square, XCircle } from 'lucide-react';
import type { AnalysisCoverageCounts, AnalysisRunFile, AnalysisRunSummary, AnalysisStageProgress } from '@/api/types';
import { useModels, useOverview, useProject } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { navigate, setParam, useRoute } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { SECTION_LABELS, STAGES, failureGroups, fileFindings, filterFiles, formatDuration, runActive, runProgress, runResumable, stageOf, statusLabel, statusTone, type FileFilter } from '@/lib/analysis';
import { Badge, Button, Callout, EmptyState, Input, Progress, Segmented, Skeleton, Tip, cx } from '@/components/ui';
import { ConfigSource } from '@/features/settings/ConfigSource';
import { AgentSettingsNotice } from '@/features/settings/AgentSettingsNotice';
import { RunChecks } from '@/features/analysis/RunChecks';

const FILE_PAGE = 200;

export function AnalysisPage() {
  const overview = useOverview();
  const project = useProject().data;
  const models = useModels().data;
  const run = overview.data?.analysis_run ?? null;
  return (
    <div className="page-scroll">
      <div className="page-inner stack-lg">
        <AnalysisHeader run={run} />
        <ConfigSource models={models} action />
        <AgentSettingsNotice />
        {overview.isLoading || !project ? (
          <Skeleton lines={8} />
        ) : !run ? (
          <EmptyState icon={Activity} title="No analysis yet">
            Analysis reads each source file for bugs, performance and security issues, then writes the project handbook in Docs. A preview shows the files, the models and the number of requests before anything is sent.
          </EmptyState>
        ) : (
          <RunDashboard run={run} coverage={overview.data?.analysis_coverage} />
        )}
      </div>
    </div>
  );
}

function AnalysisHeader({ run }: { run: AnalysisRunSummary | null }) {
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = Boolean(useBusy());
  const active = runActive(run);
  const failed = runProgress(run).failed > 0;
  // A canceled run (a canceled retry included) leaves canceled stages that a retry picks up.
  const canceled = (run?.files ?? []).some(file => file.stages.some(stage => stage.status === 'canceled' || stage.status === 'interrupted'));
  // One primary action: retrying what failed or was canceled, unless the
  // files changed since (analyzing the changes covers those stages too);
  // the other ways to run are in More.
  const retryable = failed || canceled;
  const retry = retryable && run?.status !== 'stale';
  const when = run?.updated_at ? timeAgo(run.updated_at) : '';
  return (
    <div className="page-head">
      <div className="grow">
        <h1 className="page-title">Analysis</h1>
        <p className="page-sub">
          {run ? (
            <span className="row-wrap">
              <Badge tone={statusTone(run.status)}>{statusLabel(run.status)}</Badge>
              {active ? 'Running now' : `Last run ${when}`}
              {run.elapsed_seconds ? <span className="faint">· {formatDuration(run.elapsed_seconds)} of model time</span> : null}
            </span>
          ) : (
            'Bugs, performance and security findings for every source file.'
          )}
        </p>
      </div>
      {project && run && active ? (
        <>
          <Button icon={Pause} onClick={() => void actions.controlAnalysis(run, worktreePath, 'pause')} disabled={busy || run.status !== 'running'}>Pause</Button>
          <Button icon={Square} variant="danger" onClick={() => void actions.controlAnalysis(run, worktreePath, 'cancel')} disabled={busy || run.status === 'canceling'}>Cancel</Button>
        </>
      ) : project && run && runResumable(run) ? (
        <>
          <Button icon={Square} variant="ghost" onClick={() => void actions.controlAnalysis(run, worktreePath, 'cancel')} disabled={busy}>Cancel</Button>
          <Button icon={Play} variant="primary" onClick={() => void actions.runAnalysis(project, worktreePath, { resume: run })} disabled={busy}>Resume</Button>
        </>
      ) : project ? (
        <>
          <MoreRuns
            choices={[
              ...(retry ? [{ label: run ? 'Analyze changes' : 'Analyze project', detail: 'Analyze every file whose saved results are missing, failed or out of date.', run: () => void actions.runAnalysis(project, worktreePath) }] : []),
              ...(retryable && !retry ? [{ label: failed ? 'Retry failed only' : 'Retry canceled only', detail: 'Analyze again only the stages that failed, went stale or were canceled; every other file keeps its results.', run: () => void actions.runAnalysis(project, worktreePath, { retryFailed: true }) }] : []),
              { label: 'Re-analyze every file', detail: 'Ignore the saved results and analyze every file again.', run: () => void actions.runAnalysis(project, worktreePath, { refresh: true }) },
            ]}
            disabled={busy}
          />
          {retry ? (
            <Tip label="Analyze again only the stages that failed, went stale or were canceled; every other file keeps its results">
              <Button icon={RotateCcw} variant="primary" onClick={() => void actions.runAnalysis(project, worktreePath, { retryFailed: true })} disabled={busy}>{failed ? 'Retry failed' : 'Retry canceled'}</Button>
            </Tip>
          ) : (
            <Button icon={Play} variant="primary" onClick={() => void actions.runAnalysis(project, worktreePath)} disabled={busy}>{run ? 'Analyze changes' : 'Analyze project'}</Button>
          )}
        </>
      ) : null}
    </div>
  );
}

/** The other ways to start an analysis, each saying what it does. */
function MoreRuns({ choices, disabled }: { choices: { label: string; detail: string; run: () => void }[]; disabled: boolean }) {
  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <Button variant="ghost" iconRight={ChevronDown} disabled={disabled} aria-label="More ways to analyze">More</Button>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content className="menu run-menu" align="end" sideOffset={6}>
          {choices.map(choice => (
            <Menu.Item key={choice.label} className="menu-item menu-item-detailed" onSelect={choice.run}>
              <span className="strong small">{choice.label}</span>
              <span className="xs muted">{choice.detail}</span>
            </Menu.Item>
          ))}
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  );
}

function RunDashboard({ run, coverage }: { run: AnalysisRunSummary; coverage?: Record<string, number> }) {
  const progress = runProgress(run);
  const failures = failureGroups(run);
  const files = run.files ?? [];
  const findings = files.reduce((sum, file) => sum + fileFindings(file), 0);
  const active = runActive(run);
  const kept = (run.plan?.files ?? []).filter(file => file.retained).length;
  return (
    <>
      {run.reason ? <Callout tone={statusTone(run.status) === 'danger' ? 'danger' : 'warning'} icon={AlertTriangle} title={run.reason} /> : null}
      <section className="card" aria-label="Progress">
        <div className="card-body stack">
          <div className="row">
            <span className="strong">{progress.percent}%</span>
            <span className="small muted">{progress.done} of {plural(progress.total, 'stage')} finished</span>
            <span className="spacer" />
            {active ? <span className="row small tone-info"><Loader2 size={13} className="spin" aria-hidden />{plural(progress.running, 'stage')} running</span> : null}
          </div>
          <Progress value={progress.percent} tone={progress.failed ? 'warning' : active ? 'accent' : 'success'} label="Analysis progress" />
          {kept ? <p className="xs muted">This run analyzed {plural(files.length - kept, 'file')} again; the other {plural(kept, 'file')} keep their saved results, so the dashboard covers the whole project.</p> : null}
          <div className="stat-row">
            <Stat value={files.length} label={files.length === 1 ? 'file' : 'files'} />
            <Stat value={progress.succeeded} label="stages done" tone="success" />
            <Stat value={progress.failed} label="stages failed" tone={progress.failed ? 'danger' : undefined} />
            <Stat value={findings} label={findings === 1 ? 'finding' : 'findings'} />
            {coverage ? <Stat value={coverage.stale ?? 0} label="stale in project" tone={coverage.stale ? 'warning' : undefined} /> : null}
          </div>
        </div>
      </section>
      {failures.length ? <Failures run={run} /> : null}
      <div className="analysis-grid">
        {(run.sections ?? []).map(section => (
          <section key={section.category} className="card" aria-label={`${SECTION_LABELS[section.category] ?? section.category} results`}>
            <div className="card-head">
              <span className="strong">{SECTION_LABELS[section.category] ?? section.category}</span>
              <Badge tone={statusTone(section.status)}>{statusLabel(section.status)}</Badge>
              <span className="spacer" />
              {section.finding_count ? (
                <button type="button" className="link-btn small" onClick={() => navigate('/issues', { category: section.category })}>{plural(section.finding_count, 'finding')}</button>
              ) : (
                <span className="xs muted">{section.finding_count === 0 ? 'No findings' : '—'}</span>
              )}
            </div>
            <div className="card-body stack">
              <CoverageBar coverage={section.coverage} />
              <CoverageLegend coverage={section.coverage} />
            </div>
          </section>
        ))}
      </div>
      <RunChecks checks={run.checks} />
      <ModelsUsed run={run} />
      <FileTable files={files} />
    </>
  );
}

function Stat({ value, label, tone }: { value: number; label: string; tone?: 'success' | 'danger' | 'warning' }) {
  return (
    <div className="stat">
      <div className={cx('stat-value', tone && `tone-${tone}`)}>{value.toLocaleString()}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}

function Failures({ run }: { run: AnalysisRunSummary }) {
  const groups = failureGroups(run);
  const unreachable = groups.some(group => /could not be reached|not configured/.test(group.reason));
  return (
    <section className="card card-danger" aria-label="Why stages failed">
      <div className="card-head">
        <XCircle size={15} className="tone-danger" aria-hidden />
        <span className="strong">Why stages failed</span>
        <span className="spacer" />
        {unreachable ? <Button size="sm" onClick={() => navigate('/settings/models')}>Check models</Button> : null}
        <Button size="sm" variant="ghost" onClick={() => setParam('files', 'failed')}>Show failed files</Button>
      </div>
      <ul className="failure-list">
        {groups.map(group => (
          <li key={group.reason} className="failure-item">
            <span className="failure-count">{group.count}</span>
            <div className="grow stack-tight">
              <span className="small">{group.reason}</span>
              <span className="xs muted">
                {group.stages.map(stage => STAGES.find(s => s.stage === stage)?.label ?? stage).join(', ')} · {plural(group.files.length, 'file')}
              </span>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

const SEGMENTS: { key: keyof AnalysisCoverageCounts; label: string }[] = [
  { key: 'succeeded', label: 'done' },
  { key: 'partial', label: 'partial' },
  { key: 'failed', label: 'failed' },
  { key: 'running', label: 'running' },
  { key: 'pending', label: 'pending' },
  { key: 'skipped', label: 'skipped' },
  { key: 'unavailable', label: 'unavailable' },
];

function CoverageBar({ coverage }: { coverage: AnalysisCoverageCounts }) {
  const total = Math.max(coverage.total, 1);
  return (
    <div className="coverage-bar" role="img" aria-label={SEGMENTS.filter(s => coverage[s.key]).map(s => `${coverage[s.key]} ${s.label}`).join(', ') || 'Nothing planned'}>
      {SEGMENTS.filter(s => coverage[s.key] > 0).map(s => (
        <span key={s.key} className={`coverage-seg seg-${s.key}`} style={{ width: `${(coverage[s.key] / total) * 100}%` }} />
      ))}
    </div>
  );
}

function CoverageLegend({ coverage }: { coverage: AnalysisCoverageCounts }) {
  return (
    <div className="coverage-legend">
      {SEGMENTS.filter(s => coverage[s.key] > 0).map(s => (
        <span key={s.key} className="xs muted"><span className={`legend-dot seg-${s.key}`} aria-hidden />{coverage[s.key]} {s.label}</span>
      ))}
    </div>
  );
}

function ModelsUsed({ run }: { run: AnalysisRunSummary }) {
  const providers = run.plan?.providers ?? [];
  if (!providers.length) return null;
  return (
    <section className="card" aria-label="Models used">
      <div className="card-head"><Cpu size={15} aria-hidden /><span className="strong">Models used</span></div>
      <ul className="model-list">
        {providers.map(provider => (
          <li key={provider.id} className="row">
            <span className="mono small">{provider.model.model}</span>
            {provider.model.remote_provider ? <Badge tone="warning">remote · {provider.model.provider_origin}</Badge> : <Badge tone="success">local · {provider.model.provider_origin}</Badge>}
            <span className="spacer" />
            <span className="xs muted">{provider.stages.map(stage => STAGES.find(s => s.stage === stage)?.label ?? stage.replace(/_/g, ' ')).join(', ')}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function FileTable({ files }: { files: AnalysisRunFile[] }) {
  const route = useRoute();
  const filter = (route.params.get('files') as FileFilter) || 'all';
  const [query, setQuery] = useState('');
  const [limit, setLimit] = useState(FILE_PAGE);
  const visible = useMemo(() => filterFiles(files, filter, query), [files, filter, query]);
  const counts = useMemo(() => ({ failed: filterFiles(files, 'failed', '').length, findings: filterFiles(files, 'findings', '').length, active: filterFiles(files, 'active', '').length }), [files]);
  return (
    <section className="card" aria-label="Files">
      <div className="card-head">
        <FileCode2 size={15} aria-hidden />
        <span className="strong">Files</span>
        <span className="spacer" />
        <Segmented
          label="Show files"
          value={filter}
          onChange={value => setParam('files', value === 'all' ? undefined : value)}
          options={[
            { value: 'all', label: `All ${files.length}` },
            { value: 'failed', label: `Failed ${counts.failed}` },
            { value: 'findings', label: `With findings ${counts.findings}` },
            { value: 'active', label: `In progress ${counts.active}` },
          ]}
        />
        <Input icon={Search} placeholder="Filter files…" value={query} onChange={e => setQuery(e.target.value)} aria-label="Filter files" className="toolbar-search" />
      </div>
      {visible.length ? (
        <table className="table analysis-files">
          <thead>
            <tr>
              <th>File</th>
              {STAGES.map(stage => <th key={stage.stage} className="stage-col">{stage.short}</th>)}
              <th className="num">Findings</th>
            </tr>
          </thead>
          <tbody>
            {visible.slice(0, limit).map(file => (
              <tr key={file.path} className="table-row" onClick={() => navigate('/code', { file: file.path })}>
                <td className="mono small truncate" title={file.path}>{file.path}</td>
                {STAGES.map(stage => <td key={stage.stage} className="stage-col"><StageCell progress={stageOf(file, stage.stage)} label={stage.label} /></td>)}
                <td className="num small">{fileFindings(file) || ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <div className="card-body small muted">No files match.</div>
      )}
      {visible.length > limit ? (
        <div className="card-body">
          <Button size="sm" variant="ghost" onClick={() => setLimit(limit + FILE_PAGE)}>Show {Math.min(FILE_PAGE, visible.length - limit)} more of {visible.length - limit}</Button>
        </div>
      ) : null}
    </section>
  );
}

function StageCell({ progress, label }: { progress: AnalysisStageProgress | undefined; label: string }) {
  if (!progress) return <span className="faint">—</span>;
  const tone = statusTone(progress.status);
  const Icon = progress.status === 'running' ? Loader2 : tone === 'success' ? CheckCircle2 : tone === 'danger' ? XCircle : tone === 'warning' ? AlertTriangle : progress.status === 'pending' ? Clock : CircleDashed;
  const detail = `${label}: ${statusLabel(progress.status)}${progress.finding_count ? ` · ${plural(progress.finding_count, 'finding')}` : ''}${progress.cached ? ' · saved result' : ''}${progress.reason ? ` — ${progress.reason}` : ''}`;
  return (
    <Tip label={detail}>
      <span className={cx('stage-cell', `tone-${tone}`)} aria-label={detail}>
        <Icon size={14} className={progress.status === 'running' ? 'spin' : undefined} aria-hidden />
        {progress.finding_count ? <span className="xs">{progress.finding_count}</span> : null}
      </span>
    </Tip>
  );
}
