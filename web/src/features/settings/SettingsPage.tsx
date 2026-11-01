// Settings: project, trust, models and agents, usage, appearance. Analysis
// has its own page; /settings/analysis leads there.

import { useEffect } from 'react';
import { Bot, Coins, Wrench, FolderOpen, Monitor, Moon, Palette, ShieldCheck, Sparkles, Sun } from 'lucide-react';
import { useModels, useSetup, useProject, useStatus, useTrust, useUsage } from '@/api/hooks';
import type { UsageTotal } from '@/api/types';
import { costCaveat, costLabel, formatTokens } from '@/lib/usage';
import { useActions, useBusy } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { DARK_PALETTES, setUI, useUI, type Theme } from '@/app/store';
import { navigate } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge, Button, EmptyState, Segmented, Skeleton, cx } from '@/components/ui';
import { OpenProject } from './OpenProject';
import { QuarantineList } from './Quarantine';
import { AgentRoles } from './AgentRoles';
import { ConfigSource } from './ConfigSource';
import { TimeLimits } from './TimeLimits';

const SECTIONS = [
  { id: 'project', label: 'Project', icon: FolderOpen },
  { id: 'trust', label: 'Trust', icon: ShieldCheck },
  { id: 'models', label: 'Models & agents', icon: Bot },
  { id: 'usage', label: 'Usage', icon: Coins },
  { id: 'appearance', label: 'Appearance', icon: Palette },
];

export function SettingsPage({ section }: { section: string }) {
  const current = SECTIONS.find(s => s.id === section)?.id ?? 'project';
  // Analysis moved out of Settings: old links open its page.
  useEffect(() => {
    if (section === 'analysis') navigate('/analysis', {}, { replace: true });
  }, [section]);
  return (
    <div className="docs">
      <nav className="docs-nav" aria-label="Settings sections">
        <div className="docs-nav-scroll settings-nav">
          <div className="panel-title">Settings</div>
          {SECTIONS.map(s => (
            <a key={s.id} href={`#/settings/${s.id}`} className={cx('docs-link', s.id === current && 'is-active')} onClick={e => { e.preventDefault(); navigate(`/settings/${s.id}`); }}>
              <s.icon size={14} aria-hidden />
              {s.label}
            </a>
          ))}
        </div>
      </nav>
      <div className="docs-body page-scroll">
        <div className="page-inner page-narrow stack-lg">
          <SettingsSection id={current} />
        </div>
      </div>
    </div>
  );
}

function SettingsSection({ id }: { id: string }) {
  switch (id) {
    case 'project':
      return <ProjectSettings />;
    case 'trust':
      return <TrustSettings />;
    case 'models':
      return <ModelSettings />;
    case 'usage':
      return <UsageSettings />;
    default:
      return <Appearance />;
  }
}

/** What agents spent per task, with what is not tied to a task and the total. */
function UsageSettings() {
  const { data: report, isLoading } = useUsage();
  const caveat = costCaveat(report?.total.cost);
  return (
    <>
      <h1 className="page-title">Usage</h1>
      <p className="muted">Model calls and tokens agents spent in this checkout, from the agent journal. Costs are estimates from the price table in the daemon's configuration.</p>
      {isLoading ? (
        <Skeleton lines={4} />
      ) : !report || !report.total.usage.model_calls ? (
        <EmptyState icon={Coins} title="No agent usage yet">Agent runs, revisions and analysis record what they spend here.</EmptyState>
      ) : (
        <div className="card">
          <table className="table" aria-label="Usage by task">
            <thead>
              <tr><th>Task</th><th className="num">Runs</th><th className="num">Calls</th><th className="num">Tokens in</th><th className="num">Tokens out</th><th className="num">Cost</th></tr>
            </thead>
            <tbody>
              {report.tasks.map(task => (
                <tr key={task.session_id} className="table-row" onClick={() => navigate(`/tasks/${task.session_id}`)}>
                  <td className="truncate" title={task.title}>{task.title}</td>
                  <UsageCells total={task} />
                </tr>
              ))}
              {report.other.usage.model_calls ? (
                <tr>
                  <td className="muted">Other agent work (analysis, explanations, plans)</td>
                  <UsageCells total={report.other} />
                </tr>
              ) : null}
            </tbody>
            <tfoot>
              <tr>
                <td className="strong">Total</td>
                <UsageCells total={report.total} />
              </tr>
            </tfoot>
          </table>
          {caveat ? <div className="card-foot xs muted">{caveat}</div> : null}
        </div>
      )}
    </>
  );
}

function UsageCells({ total }: { total: UsageTotal }) {
  return (
    <>
      <td className="num">{total.runs}</td>
      <td className="num">{total.usage.model_calls}</td>
      <td className="num">{formatTokens(total.usage.input_tokens)}</td>
      <td className="num">{formatTokens(total.usage.output_tokens)}</td>
      <td className="num">{costLabel(total.cost) || '—'}</td>
    </>
  );
}

function ProjectSettings() {
  const project = useProject().data;
  const status = useStatus().data;
  return (
    <>
      <h1 className="page-title">Project</h1>
      {project ? (
        <div className="card">
          <div className="card-body">
            <dl className="details details-wide">
              <dt>Name</dt><dd>{project.name}</dd>
              <dt>Path</dt><dd className="mono small">{project.path}</dd>
              <dt>Type</dt><dd>{project.type}{project.build_file ? ` · ${project.build_file}` : ''}</dd>
              <dt>Files</dt><dd>{plural(project.file_count, 'file')}{project.total_lines ? ` · ${project.total_lines.toLocaleString()} lines` : ''}</dd>
              <dt>Daemon</dt><dd>v{status?.version}</dd>
            </dl>
          </div>
        </div>
      ) : null}
      <ProjectConfiguration />
      <QuarantineList />
      <OpenProject />
    </>
  );
}

const SETUP_STATES: Record<string, { tone: 'success' | 'warning' | 'danger' | 'neutral'; label: string; text: string }> = {
  configured: { tone: 'success', label: 'Project configuration', text: 'This project runs with its own configuration file.' },
  needed: { tone: 'warning', label: 'Not set up', text: 'This project uses the daemon’s configuration. Setup gives it its own models, agents and budget.' },
  skipped: { tone: 'neutral', label: 'Daemon configuration', text: 'Setup was skipped; this project uses the daemon’s configuration.' },
  invalid: { tone: 'danger', label: 'Not applied', text: 'The project’s configuration file could not be applied; the daemon’s is used.' },
};

/** Where the project's configuration comes from, and a way back into setup. */
function ProjectConfiguration() {
  const setup = useSetup().data;
  if (!setup) return null;
  const state = SETUP_STATES[setup.state] ?? SETUP_STATES.needed;
  return (
    <section className="card" aria-label="Project configuration">
      <div className="card-head">
        <Wrench size={15} aria-hidden />
        <span className="strong">Configuration</span>
        <Badge tone={state.tone}>{state.label}</Badge>
        <span className="spacer" />
        <Button size="sm" variant={setup.state === 'configured' ? 'secondary' : 'primary'} onClick={() => navigate('/setup')}>{setup.state === 'configured' ? 'Edit in setup' : 'Run setup'}</Button>
      </div>
      <div className="card-body stack">
        <p className="small">{state.text}</p>
        {setup.error ? <p className="small tone-danger">{setup.error}</p> : null}
        <dl className="details details-wide">
          <dt>Project file</dt><dd className="mono small">{setup.config_file}</dd>
          <dt>Daemon file</dt><dd className="mono small">{setup.daemon_config_file || 'none — built-in local defaults'}</dd>
        </dl>
      </div>
    </section>
  );
}

function TrustSettings() {
  const project = useProject().data;
  const trust = useTrust().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = useBusy();
  return (
    <>
      <h1 className="page-title">Trust</h1>
      <p className="muted">Agents verify their work by running this project's tests and lint. That executes project code, so it needs your permission. Commands always run in an isolated copy of the project, never in your checkout.</p>
      <div className="card">
        <div className="card-head">
          <ShieldCheck size={15} className={trust?.trusted ? 'tone-success' : 'faint'} aria-hidden />
          <span className="strong">{trust?.trusted ? 'Checks are allowed' : 'Checks are not allowed'}</span>
          <span className="spacer" />
          {project ? (
            <Button variant={trust?.trusted ? 'ghost' : 'primary'} onClick={() => void actions.setTrust(project, worktreePath, trust, !trust?.trusted)} disabled={Boolean(busy)}>
              {trust?.trusted ? 'Revoke' : 'Allow checks'}
            </Button>
          ) : null}
        </div>
        <div className="card-body">
          <div className="label-row">Commands that will run</div>
          <ul className="commands">{(trust?.commands ?? []).map((argv, i) => <li key={i} className="mono small">{argv.join(' ')}</li>)}</ul>
          <p className="xs muted">Trust is remembered for this project path until you revoke it; worktrees ask separately.</p>
        </div>
      </div>
    </>
  );
}

function ModelSettings() {
  const models = useModels().data;
  const setup = useSetup().data;
  const ownFile = setup?.state === 'configured';
  return (
    <>
      <h1 className="page-title">Models & agents</h1>
      <p className="muted">
        {ownFile ? <>Model scopes come from this project’s <span className="mono">{setup.config_file}</span>.</> : <>Model scopes come from the daemon’s configuration.</>}
        {' '}Change them in setup; it applies them at once, once no analysis or agent work is running. Time limits are set below. Choose which model each agent role uses below; remote models receive bounded project context and always ask before each run.
      </p>
      <ConfigSource models={models} />
      <div className="card">
        <div className="card-head">
          <Sparkles size={15} aria-hidden />
          <span className="strong">Model scopes</span>
          <span className="spacer" />
          {setup ? <Button size="sm" variant="secondary" onClick={() => navigate('/setup')}>Change models</Button> : null}
        </div>
        <table className="table">
          <thead><tr><th>Scope</th><th>Model</th><th>Where</th><th>Timeout</th></tr></thead>
          <tbody>
            {Object.values(models?.scopes ?? {}).map(m => (
              <tr key={m.scope}>
                <td className="strong-row">{m.scope}</td>
                <td className="mono small">{m.model}</td>
                <td>{m.remote_provider ? <Badge tone="warning">remote · {m.provider_origin}</Badge> : <Badge tone="success">local</Badge>}</td>
                <td className="small muted">{m.timeout}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {setup ? <TimeLimits key={`${setup.project_revision}:${JSON.stringify(setup.timeouts)}`} setup={setup} /> : null}
      <AgentRoles />
    </>
  );
}

function Appearance() {
  const theme = useUI(s => s.theme);
  const palette = useUI(s => s.darkPalette);
  const layout = useUI(s => s.diffLayout);
  return (
    <>
      <h1 className="page-title">Appearance</h1>
      <div className="card">
        <div className="card-body stack-lg">
          <div className="row"><span className="grow">Theme</span><Segmented<Theme> label="Theme" value={theme} onChange={value => setUI({ theme: value })} options={[{ value: 'system', label: 'System', icon: Monitor }, { value: 'dark', label: 'Dark', icon: Moon }, { value: 'light', label: 'Light', icon: Sun }]} /></div>
          <div className="stack">
            <div className="row">
              <span className="grow">Dark palette</span>
              {theme === 'light' ? <span className="xs muted">Used when the theme is dark</span> : null}
            </div>
            <div className="palette-grid" role="radiogroup" aria-label="Dark palette">
              {DARK_PALETTES.map(option => (
                <button
                  key={option.id}
                  type="button"
                  role="radio"
                  aria-checked={palette === option.id}
                  className={cx('palette-swatch', palette === option.id && 'is-on')}
                  onClick={() => setUI({ darkPalette: option.id, theme: theme === 'light' ? 'dark' : theme })}
                >
                  <span className="swatch-preview" style={{ background: option.chrome }} aria-hidden>
                    <span className="swatch-panel" style={{ background: option.surface }}>
                      <span className="swatch-accent" style={{ background: option.accent }} />
                    </span>
                  </span>
                  <span className="small">{option.label}</span>
                </button>
              ))}
            </div>
          </div>
          <div className="row"><span className="grow">Diff layout</span><Segmented label="Diff layout" value={layout} onChange={value => setUI({ diffLayout: value })} options={[{ value: 'unified', label: 'Unified' }, { value: 'split', label: 'Split' }]} /></div>
        </div>
      </div>
    </>
  );
}
