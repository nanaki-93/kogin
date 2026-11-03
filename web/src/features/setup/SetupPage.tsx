// Project setup: a wizard that writes the project's own configuration
// (.kogin/config.yaml) — its models, which model each agent role uses, the
// run budget and the time limits — then lets the user allow the project's checks and choose
// what analysis reads. It opens the first time a project without that file
// is opened; skipping keeps the daemon's configuration.

import { useMemo, useState } from 'react';
import { ArrowLeft, ArrowRight, Bot, Check, CheckCircle2, Clock, Cpu, FlaskConical, FolderMinus, Loader2, Play, Plug, ShieldCheck, Sparkles, XCircle } from 'lucide-react';
import type { ProjectSetup, SetupModel, SetupModelTest, SetupScope } from '@/api/types';
import { useAnalysisSelection, useAvailableModels, useMainProject, useSetup, useTrust } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { navigate, setParam, useRoute } from '@/lib/router';
import { LIMITS, ROLES, limitErrors } from '@/lib/agentSettings';
import { LIMIT_MAXIMA, PROVIDERS, SCOPES, SKIPPED_FOLDERS, folderGroups, modelHint, modelProblem, modelSuggestions, providerOf, timeoutErrors, withProvider } from '@/lib/setup';
import { plural } from '@/lib/util';
import { Badge, Button, Callout, EmptyState, Field, Input, Select, Skeleton, cx } from '@/components/ui';
import { AgentSettingsNotice } from '@/features/settings/AgentSettingsNotice';
import { TimeoutFields } from '@/features/settings/TimeLimits';

const STEPS = [
  { id: 'models', label: 'Models', icon: Cpu },
  { id: 'agents', label: 'Agents & budget', icon: Bot },
  { id: 'checks', label: 'Checks', icon: FlaskConical },
  { id: 'analysis', label: 'Analysis', icon: Sparkles },
] as const;

type StepId = (typeof STEPS)[number]['id'];
type Draft = Pick<ProjectSetup, 'models' | 'roles' | 'limits' | 'timeouts'>;

export function SetupPage() {
  const setup = useSetup();
  if (setup.isLoading) return <div className="page-scroll"><div className="page-inner page-narrow"><Skeleton lines={8} /></div></div>;
  if (!setup.data) {
    return (
      <EmptyState icon={Plug} title="Setup is unavailable" action={<Button icon={ArrowLeft} onClick={() => navigate('/')}>Inbox</Button>}>
        Open a project first; setup configures the active project.
      </EmptyState>
    );
  }
  // A saved configuration or another project starts a fresh draft.
  return <SetupWizard key={`${setup.data.project_revision}:${setup.data.config_file}`} setup={setup.data} />;
}

function SetupWizard({ setup }: { setup: ProjectSetup }) {
  const route = useRoute();
  const project = useMainProject().data;
  const actions = useActions();
  const busy = Boolean(useBusy());
  const step = (STEPS.find(s => s.id === route.params.get('step'))?.id ?? 'models') as StepId;
  const index = STEPS.findIndex(s => s.id === step);
  const [draft, setDraft] = useState<Draft>({ models: setup.models, roles: setup.roles, limits: setup.limits, timeouts: setup.timeouts });
  const go = (next: StepId) => setParam('step', next === 'models' ? undefined : next);

  const modelProblems = SCOPES.map(({ scope, label }) => (modelProblem(draft.models[scope]) ? `${label}: ${modelProblem(draft.models[scope]).toLowerCase()}` : '')).filter(Boolean);
  const budgetErrors = { ...limitErrors(draft.limits, LIMIT_MAXIMA), ...timeoutErrors(draft.timeouts) };

  async function saveAndContinue() {
    if (await actions.saveSetup(setup, draft)) go('checks');
  }
  async function skip() {
    if (await actions.skipSetup(setup)) navigate('/');
  }

  return (
    <div className="page-scroll">
      <div className="page-inner setup-page stack-lg">
        <div className="page-head">
          <div className="grow">
            <h1 className="page-title">Set up {project?.name || 'this project'}</h1>
            <p className="page-sub">
              Kogin saves these settings in <span className="mono">.kogin/config.yaml</span> inside the project, kept out of Git, and applies them at once.
              {setup.daemon_config_file ? <> They start from <span className="mono">{setup.daemon_config_file}</span>.</> : null}
            </p>
          </div>
          {setup.state === 'needed' ? <Button variant="ghost" onClick={() => void skip()} disabled={busy}>Skip and use the daemon’s settings</Button> : null}
        </div>
        {setup.state === 'invalid' ? (
          <Callout tone="danger" icon={XCircle} title="The project’s configuration file could not be applied">
            {setup.error} Saving here replaces the settings it shows and keeps its other keys.
          </Callout>
        ) : null}
        <AgentSettingsNotice note="The roles and budget saved in Settings → Models & agents belong to an earlier model configuration. Saving setup replaces them; until then, analysis and agents wait. Connection tests work either way." />
        <ol className="setup-steps" aria-label="Setup steps">
          {STEPS.map((s, i) => (
            <li key={s.id}>
              <button type="button" className={cx('setup-step', s.id === step && 'is-active', i < index && 'is-done')} onClick={() => go(s.id)} aria-current={s.id === step ? 'step' : undefined}>
                <span className="setup-step-mark">{i < index ? <Check size={13} aria-hidden /> : i + 1}</span>
                <s.icon size={14} aria-hidden />
                {s.label}
              </button>
            </li>
          ))}
        </ol>

        {step === 'models' ? (
          <>
            <ModelsStep setup={setup} models={draft.models} onChange={models => setDraft(d => ({ ...d, models }))} />
            <div className="setup-footer">
              {modelProblems.length ? <span className="small tone-warning">{modelProblems[0]}</span> : null}
              <span className="spacer" />
              <Button variant="primary" iconRight={ArrowRight} onClick={() => go('agents')} disabled={Boolean(modelProblems.length)}>Next</Button>
            </div>
          </>
        ) : step === 'agents' ? (
          <>
            <AgentsStep draft={draft} errors={budgetErrors} onChange={patch => setDraft(d => ({ ...d, ...patch }))} />
            <div className="setup-footer">
              <Button variant="ghost" icon={ArrowLeft} onClick={() => go('models')}>Back</Button>
              <span className="spacer" />
              <Button variant="primary" icon={Check} onClick={() => void saveAndContinue()} disabled={busy || Boolean(modelProblems.length) || Boolean(Object.keys(budgetErrors).length)}>Save and continue</Button>
            </div>
          </>
        ) : step === 'checks' ? (
          <>
            <ChecksStep />
            <div className="setup-footer">
              <Button variant="ghost" icon={ArrowLeft} onClick={() => go('agents')}>Back</Button>
              <span className="spacer" />
              <Button variant="primary" iconRight={ArrowRight} onClick={() => go('analysis')}>Next</Button>
            </div>
          </>
        ) : (
          <AnalysisStep onBack={() => go('checks')} />
        )}
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ models

function ModelsStep({ setup, models, onChange }: { setup: ProjectSetup; models: Record<SetupScope, SetupModel>; onChange: (models: Record<SetupScope, SetupModel>) => void }) {
  const usesCli = SCOPES.some(({ scope }) => providerOf(models[scope]).cli);
  const catalog = useAvailableModels(usesCli).data;
  return (
    <div className="stack-lg">
      <p className="muted">Kogin uses three models. Each can be a local server, a cloud API or an installed CLI; one model can serve all three.</p>
      {SCOPES.map(({ scope, label, use }, i) => (
        <ModelCard
          key={scope}
          setup={setup}
          scope={scope}
          label={label}
          use={use}
          model={models[scope]}
          suggestions={modelSuggestions(catalog, models[scope])}
          copyFrom={i > 0 ? { label: SCOPES[0].label, model: models.analyze } : undefined}
          onChange={model => onChange({ ...models, [scope]: model })}
        />
      ))}
    </div>
  );
}

function ModelCard({ setup, scope, label, use, model, suggestions, copyFrom, onChange }: {
  setup: ProjectSetup;
  scope: SetupScope;
  label: string;
  use: string;
  model: SetupModel;
  suggestions: string[];
  copyFrom?: { label: string; model: SetupModel };
  onChange: (model: SetupModel) => void;
}) {
  const actions = useActions();
  const [test, setTest] = useState<SetupModelTest | null>(null);
  const [testing, setTesting] = useState(false);
  const info = providerOf(model);
  const id = (field: string) => `${scope}-${field}`;
  const set = (patch: Partial<SetupModel>) => {
    setTest(null);
    onChange({ ...model, ...patch });
  };
  const problem = modelProblem(model);

  async function runTest() {
    setTesting(true);
    setTest(await actions.testSetupModel(setup, scope, model));
    setTesting(false);
  }

  return (
    <section className="card" aria-label={`${label} model`}>
      <div className="card-head">
        <span className="strong">{label}</span>
        <span className="xs muted truncate">{use}</span>
        <span className="spacer" />
        {copyFrom ? <Button size="sm" variant="ghost" onClick={() => { setTest(null); onChange({ ...copyFrom.model }); }}>Same as {copyFrom.label}</Button> : null}
      </div>
      <div className="card-body setup-grid">
        <Field label="Provider" htmlFor={id('provider')} hint={info.hint}>
          <Select id={id('provider')} value={model.provider || 'openai'} onChange={e => { setTest(null); onChange(withProvider(model, e.target.value)); }}>
            {PROVIDERS.map(p => <option key={p.id} value={p.id}>{p.label}</option>)}
          </Select>
        </Field>
        <Field label="Model" htmlFor={id('model')} hint={modelHint(model, suggestions.length)}>
          <Input id={id('model')} value={model.model} onChange={e => set({ model: e.target.value })} list={suggestions.length ? id('models') : undefined} placeholder="model name" autoComplete="off" />
          {suggestions.length ? <datalist id={id('models')}>{suggestions.map(s => <option key={s} value={s} />)}</datalist> : null}
        </Field>
        {info.address ? (
          <Field label="API address" htmlFor={id('address')} hint="Loopback HTTP or HTTPS, ending in /v1.">
            <Input id={id('address')} value={model.api_base_url ?? ''} onChange={e => set({ api_base_url: e.target.value })} placeholder="http://localhost:1234/v1" />
          </Field>
        ) : null}
        {info.key ? (
          <Field label={info.key === 'required' ? 'API key' : 'API key (optional)'} htmlFor={id('key')} hint={model.api_key_set && !model.api_key && !model.clear_api_key ? 'A key is saved; leave empty to keep it.' : 'Saved only in the project’s private configuration file.'}>
            <div className="row">
              <Input id={id('key')} type="password" value={model.api_key ?? ''} onChange={e => set({ api_key: e.target.value, clear_api_key: false })} placeholder={model.api_key_set && !model.clear_api_key ? '•••••••• saved' : ''} autoComplete="new-password" />
              {model.api_key_set && !model.api_key ? <Button size="sm" variant="ghost" onClick={() => set({ clear_api_key: !model.clear_api_key })}>{model.clear_api_key ? 'Keep key' : 'Remove key'}</Button> : null}
            </div>
          </Field>
        ) : null}
        {info.vertex ? (
          <>
            <Field label="Google Cloud project" htmlFor={id('vproject')}>
              <Input id={id('vproject')} value={model.vertex_project ?? ''} onChange={e => set({ vertex_project: e.target.value })} />
            </Field>
            <Field label="Location" htmlFor={id('vlocation')}>
              <Input id={id('vlocation')} value={model.vertex_location ?? ''} onChange={e => set({ vertex_location: e.target.value })} placeholder="us-central1" />
            </Field>
          </>
        ) : null}
        {info.efforts ? (
          <Field label="Reasoning effort" htmlFor={id('effort')}>
            <Select id={id('effort')} value={model.reasoning_effort ?? ''} onChange={e => set({ reasoning_effort: e.target.value || undefined })}>
              {info.efforts.map(effort => <option key={effort} value={effort}>{effort || 'Model default'}</option>)}
            </Select>
          </Field>
        ) : null}
        {info.cli ? (
          <Field label="CLI path (optional)" htmlFor={id('cli')} hint={`Defaults to ${info.id} on the daemon’s PATH.`}>
            <Input id={id('cli')} value={model.cli_path ?? ''} onChange={e => set({ cli_path: e.target.value || undefined })} placeholder={info.id} />
          </Field>
        ) : null}
      </div>
      <div className="card-body setup-test">
        <Button size="sm" icon={testing ? Loader2 : Plug} onClick={() => void runTest()} disabled={Boolean(problem) || testing} loading={testing}>Test connection</Button>
        <TestResult test={test} />
      </div>
    </section>
  );
}

function TestResult({ test }: { test: SetupModelTest | null }) {
  if (!test) return <span className="xs muted">Sends one short request; remote providers receive no project code.</span>;
  if (test.ok) {
    return (
      <span className="row small tone-success">
        <CheckCircle2 size={14} aria-hidden />
        Connected{test.model ? ` to ${test.model}` : ''} · {(test.latency_ms / 1000).toFixed(1)} s
      </span>
    );
  }
  return (
    <span className="stack-tight small">
      <span className="row tone-danger"><XCircle size={14} aria-hidden />{test.reason}</span>
      {test.detail ? <span className="xs muted mono setup-detail">{test.detail}</span> : null}
    </span>
  );
}

// ------------------------------------------------------------------ agents

function AgentsStep({ draft, errors, onChange }: { draft: Draft; errors: Record<string, string>; onChange: (patch: Partial<Draft>) => void }) {
  return (
    <div className="stack-lg">
      <section className="card" aria-label="Agent roles">
        <div className="card-head"><Bot size={15} aria-hidden /><span className="strong">Which model each agent uses</span></div>
        <div className="card-body setup-grid">
          {ROLES.map(({ role, label, hint }) => (
            <Field key={role} label={label} htmlFor={`setup-role-${role}`} hint={hint}>
              <Select id={`setup-role-${role}`} value={draft.roles[role]} onChange={e => onChange({ roles: { ...draft.roles, [role]: e.target.value } })}>
                {SCOPES.map(({ scope, label: scopeLabel }) => <option key={scope} value={scope}>{scopeLabel} · {draft.models[scope].model || 'not set'}</option>)}
              </Select>
            </Field>
          ))}
        </div>
        <div className="card-body xs muted">Settings → Models & agents can later point a role at any model an installed CLI lists.</div>
      </section>
      <section className="card" aria-label="Budget per run">
        <div className="card-head"><ShieldCheck size={15} aria-hidden /><span className="strong">Budget per run</span><span className="xs muted">Agents stop when a run reaches any limit.</span></div>
        <div className="card-body limit-grid">
          {LIMITS.map(({ key, label, min }) => (
            <Field key={key} label={label} htmlFor={`setup-limit-${key}`} error={errors[key]}>
              <Input id={`setup-limit-${key}`} type="number" inputMode="numeric" min={min} max={LIMIT_MAXIMA[key]} step={1} value={Number.isNaN(draft.limits[key]) ? '' : String(draft.limits[key] ?? '')} invalid={Boolean(errors[key])} onChange={e => onChange({ limits: { ...draft.limits, [key]: e.target.value === '' ? Number.NaN : Number(e.target.value) } })} />
            </Field>
          ))}
        </div>
      </section>
      <section className="card" aria-label="Time limits">
        <div className="card-head"><Clock size={15} aria-hidden /><span className="strong">Time limits</span><span className="xs muted">A request or check that takes longer stops; raise them for slow models.</span></div>
        <div className="card-body">
          <TimeoutFields id="setup-timeout" timeouts={draft.timeouts} onChange={timeouts => onChange({ timeouts })} />
        </div>
      </section>
    </div>
  );
}

// ------------------------------------------------------------------ checks

function ChecksStep() {
  const trust = useTrust().data;
  const project = useMainProject().data;
  const actions = useActions();
  const busy = Boolean(useBusy());
  if (!trust || !project) return <Skeleton lines={4} />;
  return (
    <section className="card" aria-label="Checks">
      <div className="card-head">
        <FlaskConical size={15} aria-hidden />
        <span className="strong">Tests and lint</span>
        <span className="spacer" />
        {trust.trusted ? <Badge tone="success" icon={ShieldCheck}>Allowed</Badge> : <Badge tone="warning">Not allowed yet</Badge>}
      </div>
      <div className="card-body stack">
        <p className="small">Agents verify their work by running these commands in an isolated copy of the project. They run only after you allow them, and you can revoke that in Settings → Trust.</p>
        {trust.commands.length ? (
          <ul className="commands">{trust.commands.map((argv, i) => <li key={i} className="mono small">{argv.join(' ')}</li>)}</ul>
        ) : (
          <p className="small muted">No check commands were detected for this project.</p>
        )}
        <div className="row">
          {trust.trusted ? (
            <Button variant="ghost" onClick={() => void actions.setTrust(project, '', trust, false)} disabled={busy}>Revoke</Button>
          ) : (
            <Button variant="primary" icon={ShieldCheck} onClick={() => void actions.setTrust(project, '', trust, true)} disabled={busy || !trust.commands.length}>Allow these checks</Button>
          )}
        </div>
      </div>
    </section>
  );
}

// ------------------------------------------------------------------ analysis

function AnalysisStep({ onBack }: { onBack: () => void }) {
  const selection = useAnalysisSelection(true).data;
  const project = useMainProject().data;
  const actions = useActions();
  const busy = Boolean(useBusy());
  const groups = useMemo(() => folderGroups((selection?.files ?? []).filter(f => !f.reason).map(f => f.path)), [selection]);
  const [excluded, setExcluded] = useState<Set<string> | null>(null);
  const current = excluded ?? new Set(selection?.excluded_paths ?? []);
  if (!selection || !project) return <Skeleton lines={5} />;

  const toggle = (files: string[], exclude: boolean) => {
    const next = new Set(current);
    for (const file of files) {
      if (exclude) next.add(file);
      else next.delete(file);
    }
    setExcluded(next);
  };
  const included = groups.reduce((sum, group) => sum + group.files.filter(f => !current.has(f)).length, 0);

  async function finish(analyze: boolean) {
    if (!selection || !project) return;
    if (!(await actions.saveAnalysisSelection(selection, [...current].sort()))) return;
    navigate(analyze ? '/analysis' : '/');
    if (analyze) await actions.runAnalysis(project, '');
  }

  return (
    <>
      <section className="card" aria-label="What analysis reads">
        <div className="card-head">
          <FolderMinus size={15} aria-hidden />
          <span className="strong">What analysis reads</span>
          <span className="spacer" />
          <span className="xs muted">{plural(included, 'file')} included</span>
        </div>
        <div className="card-body stack">
          <p className="small">Leave out folders that are not your code, such as dependencies, generated files or fixtures. Files ignored by .gitignore or .koginignore are already left out.</p>
          {!selection.editable ? <Callout tone="warning" title="An analysis is running">Exclusions can be changed when it finishes.</Callout> : null}
          <ul className="folder-list">
            {groups.map(group => {
              const out = group.files.every(f => current.has(f));
              const suggested = SKIPPED_FOLDERS.includes(group.folder);
              return (
                <li key={group.folder || '.'}>
                  <label className="check-label folder-row">
                    <input type="checkbox" checked={!out} onChange={e => toggle(group.files, !e.target.checked)} disabled={!selection.editable} />
                    <span className="mono small grow">{group.folder ? `${group.folder}/` : 'Files in the project root'}</span>
                    {suggested && !out ? <Badge tone="warning">usually skipped</Badge> : null}
                    <span className="xs muted">{plural(group.files.length, 'file')}</span>
                  </label>
                </li>
              );
            })}
          </ul>
        </div>
      </section>
      <div className="setup-footer">
        <Button variant="ghost" icon={ArrowLeft} onClick={onBack}>Back</Button>
        <span className="spacer" />
        <Button onClick={() => void finish(false)} disabled={busy || !selection.editable}>Finish</Button>
        <Button variant="primary" icon={Play} onClick={() => void finish(true)} disabled={busy || !selection.editable || !included}>Finish and analyze</Button>
      </div>
    </>
  );
}
