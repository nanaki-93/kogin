// Health: is the project fit for its goal, and is fixing converging? The
// finish line from the profile, the tests as they run with coverage, code
// nothing calls, repeated code, functions over budget, the found/fixed
// trend and tasks that would improve it.

import { CheckCircle2, CircleAlert, Copy, FlaskConical, Flag, GitFork, HeartPulse, Play, Trash2, TrendingDown, TrendingUp, Wrench } from 'lucide-react';
import type { HealthTask, ProjectHealth, ProjectProfile } from '@/api/types';
import { useHealth, useMainProject, useMainTrust } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { navigate } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { finishLineWords } from '@/lib/profile';
import { formatDuration } from '@/lib/analysis';
import { Badge, Button, Callout, EmptyState, Skeleton, Spinner, Tip, cx } from '@/components/ui';
import { SCOPE_LABELS } from '@/components/domain';
import { TrendChart } from './TrendChart';

export function HealthPage() {
  const { data: health, isLoading, error } = useHealth();
  return (
    <div className="page-scroll">
      <div className="page-inner stack-lg">
        <HealthHeader health={health} />
        {isLoading ? (
          <Skeleton lines={8} />
        ) : !health ? (
          <EmptyState icon={HeartPulse} title="No health yet">{error instanceof Error ? error.message : 'Open a project to measure its health.'}</EmptyState>
        ) : (
          <>
            <FinishLine health={health} />
            <Trend health={health} />
            <div className="health-grid">
              <Tests health={health} />
              <CodeQuality health={health} />
            </div>
            <Tasks tasks={health.tasks} />
          </>
        )}
      </div>
    </div>
  );
}

function HealthHeader({ health }: { health: ProjectHealth | undefined }) {
  const main = useMainProject().data;
  const trusted = useMainTrust().data?.trusted;
  const actions = useActions();
  const busy = Boolean(useBusy());
  const running = Boolean(health?.tests_running);
  const runTests = main && trusted ? () => void actions.runHealthTests(main) : undefined;
  return (
    <div className="page-head">
      <div className="grow">
        <h1 className="page-title">Health</h1>
        <p className="page-sub">Is the project fit for its goal, and does fixing converge? Measured from the code and its tests, without a model.</p>
      </div>
      <Button variant="ghost" icon={Flag} onClick={() => navigate('/settings/profile')}>Profile</Button>
      <Tip label={trusted ? 'Runs the tests in a copy of the project, with coverage for Go modules.' : 'Trust local execution for this project in Settings → Trust to run its tests.'}>
        <Button variant={health?.tests?.current ? 'ghost' : 'primary'} icon={running ? undefined : Play} onClick={runTests} disabled={!runTests || running || busy}>
          {running ? <><Spinner size={13} /> Running tests…</> : 'Run tests'}
        </Button>
      </Tip>
    </div>
  );
}

const PROFILE_KIND: Record<string, string> = { personal: 'Personal', team: 'Team', open_source: 'Open-source', enterprise: 'Enterprise' };
const PROFILE_SHAPE: Record<string, string> = { cli: 'command-line tool', gui_backend: 'app with frontend and backend', web_service: 'web service', library: 'library', other: 'project' };
const PLATFORM_LABELS: Record<string, string> = { macos: 'macOS', linux: 'Linux', windows: 'Windows' };

function profileLine(profile: ProjectProfile): string {
  const platforms = profile.platforms.length ? ` on ${profile.platforms.map(p => PLATFORM_LABELS[p] ?? p).join(', ')}` : '';
  return `${PROFILE_KIND[profile.kind] ?? profile.kind} ${PROFILE_SHAPE[profile.shape] ?? 'project'}${platforms} · ${profile.security} security`;
}

/** The finish line: the issues still to fix and the tests, against the profile. */
function FinishLine({ health }: { health: ProjectHealth }) {
  const line = health.finish_line;
  const severities = ['critical', 'high', 'medium', 'low'].filter(s => line.by_severity[s]);
  const outOfScope = Object.entries(line.out_of_scope).filter(([, count]) => count > 0);
  const testsWord = line.tests_pass === null ? 'tests not run on this version' : line.tests_pass ? 'tests pass' : 'tests fail';
  return (
    <section className={cx('card finish-line', line.done && 'is-done')} aria-label="Finish line">
      <div className="card-body finish-line-body">
        <div className="finish-line-status">
          {line.done ? <CheckCircle2 size={28} className="tone-success" aria-hidden /> : <Flag size={28} className="tone-accent" aria-hidden />}
          <div>
            <div className="finish-line-title">
              {line.done ? 'Done for its goal' : line.open ? `${plural(line.open, 'issue')} to the finish line` : 'No issue to the finish line'}
            </div>
            <div className="small muted">
              Counting {finishLineWords(line.severity)} · {testsWord}
              {!health.profile.saved ? ' · default profile' : ''}
            </div>
          </div>
          <span className="spacer" />
          {line.open ? <Button size="sm" variant="ghost" icon={CircleAlert} onClick={() => navigate('/issues')}>Open issues</Button> : null}
        </div>
        <div className="row-wrap small">
          {severities.map(s => <Badge key={s} tone={s === 'critical' || s === 'high' ? 'danger' : 'warning'}>{line.by_severity[s]} {s}</Badge>)}
          <span className="muted">{profileLine(health.profile)}</span>
          <button type="button" className="link-btn xs" onClick={() => navigate('/settings/profile')}>{health.profile.saved ? 'Edit profile' : 'Set the profile'}</button>
        </div>
        {outOfScope.length ? (
          <div className="xs muted">
            Not counted:{' '}
            {outOfScope.map(([reason, count], i) => (
              <span key={reason}>
                {i ? ' · ' : ''}
                <button type="button" className="link-btn" onClick={() => navigate('/issues', { scope: reason === 'below_finish_line' ? 'below' : 'out' })}>
                  {count} {(SCOPE_LABELS[reason] ?? reason).toLowerCase()}
                </button>
              </span>
            ))}
          </div>
        ) : null}
        {!line.done && !line.open && line.tests_pass === false ? <Callout tone="danger" icon={FlaskConical} title="The tests fail">Fix the failing tests to reach the finish line.</Callout> : null}
      </div>
    </section>
  );
}

const VERDICTS: Record<string, { title: string; text: string; tone: 'success' | 'warning' | 'neutral' }> = {
  converging: { title: 'Converging', text: 'Fixes close more issues than new analyses find.', tone: 'success' },
  growing: { title: 'Not converging', text: 'Analyses find more issues than fixes close. Count less (the profile), analyze only what changed, and keep fixes and their tests small.', tone: 'warning' },
  steady: { title: 'Steady', text: 'About as many issues are found as fixed.', tone: 'neutral' },
  quiet: { title: 'Quiet', text: 'Nothing found or fixed in the last week.', tone: 'neutral' },
};

function Trend({ health }: { health: ProjectHealth }) {
  const trend = health.trend;
  const verdict = VERDICTS[trend.verdict] ?? VERDICTS.steady;
  return (
    <section className="card" aria-label="Found and fixed">
      <div className="card-head">
        {trend.verdict === 'growing' ? <TrendingUp size={15} aria-hidden /> : <TrendingDown size={15} aria-hidden />}
        <span className="strong">Found and fixed</span>
        <Badge tone={verdict.tone}>{verdict.title}</Badge>
        <span className="xs muted grow">Last 7 days: {trend.found_7} found, {trend.fixed_7} fixed. {verdict.text}</span>
      </div>
      <div className="card-body">
        <TrendChart days={trend.days} />
      </div>
    </section>
  );
}

function Tests({ health }: { health: ProjectHealth }) {
  const tests = health.tests;
  const code = health.code;
  const ratio = code.source_lines ? code.test_lines / code.source_lines : 0;
  return (
    <section className="card" aria-label="Tests">
      <div className="card-head">
        <FlaskConical size={15} aria-hidden />
        <span className="strong">Tests</span>
        {tests ? <Badge tone={tests.passed ? 'success' : 'danger'}>{tests.passed ? 'Pass' : 'Fail'}</Badge> : null}
        {tests && !tests.current ? <Badge tone="neutral">older version</Badge> : null}
        <span className="xs muted grow">{tests ? `Ran ${timeAgo(tests.ran_at)} in ${formatDuration(tests.duration_ms / 1000)}` : 'Not run yet: Run tests measures pass/fail and coverage.'}</span>
      </div>
      <div className="card-body stack">
        <div className="stat-row">
          <HealthStat value={tests?.coverage != null ? `${tests.coverage.toFixed(0)}%` : '—'} label="covered" hint="Share of Go statements the tests run, in the packages whose tests ran." />
          <HealthStat value={formatCount(code.source_lines)} label="code lines" hint={`${plural(code.source_files, 'source file')}`} />
          <HealthStat value={formatCount(code.test_lines)} label="test lines" hint={`${plural(code.test_files, 'test file')}`} />
          <HealthStat value={ratio ? ratio.toFixed(1) : '—'} label="test : code" hint="Test lines per line of code. Past about 2, tests are likely copied instead of extended." tone={ratio > 2.5 ? 'warning' : undefined} />
        </div>
        {code.test_dirs.length ? (
          <div className="small">
            <div className="label-row">Tests spread over many files</div>
            <ul className="health-list">
              {code.test_dirs.slice(0, 5).map(dir => (
                <li key={dir.dir}><span className="mono xs">{dir.dir}/</span> <span className="muted">{dir.test_files} test files for {plural(dir.source_files, 'source file')}</span></li>
              ))}
            </ul>
          </div>
        ) : null}
        {tests?.packages.length ? (
          <table className="table health-packages" aria-label="Coverage by package">
            <thead><tr><th>Package</th><th className="num">Covered</th><th /></tr></thead>
            <tbody>
              {tests.packages.map(pkg => (
                <tr key={pkg.name}>
                  <td className="mono xs truncate">{pkg.name}</td>
                  <td className="num xs">{pkg.coverage != null ? `${pkg.coverage.toFixed(0)}%` : pkg.no_tests ? 'no tests' : '—'}</td>
                  <td className="xs">{pkg.passed ? <span className="cover-bar" aria-hidden><span style={{ width: `${Math.min(100, pkg.coverage ?? 0)}%` }} /></span> : <span className="tone-danger">fails</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : null}
        {tests?.commands.filter(c => c.output).map((command, i) => (
          <details key={i} className="issue-more">
            <summary className="xs muted">Output of <span className="mono">{command.command.join(' ')}</span>{command.dir ? ` in ${command.dir}` : ''}</summary>
            <pre className="output">{command.output}</pre>
          </details>
        ))}
      </div>
    </section>
  );
}

function CodeQuality({ health }: { health: ProjectHealth }) {
  const dup = health.duplication;
  const percent = (repeated: number, total: number) => (total ? `${((repeated / total) * 100).toFixed(1)}%` : '—');
  return (
    <section className="card" aria-label="Code">
      <div className="card-head">
        <GitFork size={15} aria-hidden />
        <span className="strong">Code</span>
        <span className="xs muted grow">Repetition, code nothing calls and functions over their budget.</span>
      </div>
      <div className="card-body stack">
        <div className="stat-row">
          <HealthStat value={percent(dup.repeated_lines, dup.total_lines)} label="repeated" hint={`${dup.repeated_lines} of ${dup.total_lines} lines of code are copies of a block of 6+ lines.`} tone={dup.repeated_lines / Math.max(1, dup.total_lines) > 0.05 ? 'warning' : undefined} />
          <HealthStat value={percent(dup.test_repeated_lines, dup.test_total_lines)} label="repeated in tests" hint={`${dup.test_repeated_lines} of ${dup.test_total_lines} test lines.`} />
          <HealthStat value={String(health.unused.length)} label="unused" hint="Functions nothing calls from main, init or package-level code (Go)." tone={health.unused.length ? 'warning' : undefined} />
          <HealthStat value={String(health.complexity.length)} label="over budget" hint="Functions over their complexity or length budget (AGENTS.md; complexity 15 by default)." tone={health.complexity.length ? 'warning' : undefined} />
        </div>
        {dup.groups.length ? (
          <div className="small">
            <div className="label-row"><Copy size={12} aria-hidden /> Repeated blocks</div>
            <ul className="health-list">
              {dup.groups.slice(0, 5).map((group, i) => (
                <li key={i}>
                  <span className="muted">{group.lines} lines ×{group.places.length}:</span>{' '}
                  {group.places.map((place, j) => (
                    <span key={j}>{j ? ', ' : ''}<button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: place.path, line: String(place.start_line) })}>{place.path}:{place.start_line}</button></span>
                  ))}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {health.unused.length ? (
          <div className="small">
            <div className="label-row"><Trash2 size={12} aria-hidden /> Nothing calls</div>
            <ul className="health-list">
              {health.unused.slice(0, 8).map(fn => (
                <li key={fn.path + fn.function}>
                  <button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: fn.path, line: String(fn.start_line) })}>{fn.function}</button>{' '}
                  <span className="muted xs">{fn.path}{fn.test_only ? ' · only tests call it' : ''}</span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {health.complexity.length ? (
          <div className="small">
            <div className="label-row">Over budget</div>
            <ul className="health-list">
              {health.complexity.slice(0, 6).map(fn => (
                <li key={fn.path + fn.function}>
                  <button type="button" className="link-btn mono xs" onClick={() => navigate('/code', { file: fn.path, line: String(fn.start_line) })}>{fn.function}</button>{' '}
                  <span className="muted xs">complexity {fn.complexity} / {fn.budget} · {fn.lines} lines</span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </div>
    </section>
  );
}

/** Tasks that would improve the health: a draft opens in the composer, nothing is saved before. */
function Tasks({ tasks }: { tasks: HealthTask[] }) {
  const actions = useActions();
  return (
    <section className="card" aria-label="Improve the health">
      <div className="card-head">
        <Wrench size={15} aria-hidden />
        <span className="strong">Improve the health</span>
        <span className="xs muted grow">Each is one small task; Draft opens it in the composer.</span>
      </div>
      {tasks.length ? (
        <ul className="health-tasks">
          {tasks.map(task => (
            <li key={task.id}>
              <div className="grow">
                <div className="strong-row">{task.title}</div>
                <div className="xs muted">{task.why}</div>
              </div>
              <Button size="sm" variant="ghost" icon={Wrench} onClick={() => actions.startHealthTask(task)}>Draft task</Button>
            </li>
          ))}
        </ul>
      ) : (
        <div className="card-body small muted">Nothing to improve: no unused code, repeated blocks, spread tests or functions over budget.</div>
      )}
    </section>
  );
}

function HealthStat({ value, label, hint, tone }: { value: string; label: string; hint: string; tone?: 'warning' }) {
  return (
    <Tip label={hint}>
      <div className={cx('stat', tone && `stat-${tone}`)} tabIndex={0}>
        <div className="stat-value">{value}</div>
        <div className="stat-label">{label}</div>
      </div>
    </Tip>
  );
}

function formatCount(n: number): string {
  return n >= 10_000 ? `${(n / 1000).toFixed(0)}k` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}
