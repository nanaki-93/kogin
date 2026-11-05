// Settings → Profile: what the project is for, so issues are judged against
// its own goal: kind, shape, platforms, security level and the finish line.

import { useEffect, useState } from 'react';
import { Flag, Target } from 'lucide-react';
import type { FinishSeverity, ProjectKind, ProjectPlatform, ProjectProfile, ProjectShape, SecurityLevel } from '@/api/types';
import { useMainProject, useProfile } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { navigate } from '@/lib/router';
import { Button, Callout, Field, Input, Segmented, Select, Skeleton } from '@/components/ui';
import { KIND_DEFAULTS, SECURITY_SCOPES, profileChanged } from '@/lib/profile';

const KINDS: { value: ProjectKind; label: string }[] = [
  { value: 'personal', label: 'Personal' },
  { value: 'team', label: 'Team' },
  { value: 'open_source', label: 'Open source' },
  { value: 'enterprise', label: 'Enterprise' },
];
const SHAPES: { value: ProjectShape; label: string }[] = [
  { value: 'cli', label: 'Command-line tool' },
  { value: 'gui_backend', label: 'App with its own frontend and backend' },
  { value: 'web_service', label: 'Web service' },
  { value: 'library', label: 'Library' },
  { value: 'other', label: 'Other' },
];
const PLATFORMS: { value: ProjectPlatform; label: string }[] = [
  { value: 'macos', label: 'macOS' },
  { value: 'linux', label: 'Linux' },
  { value: 'windows', label: 'Windows' },
];
const SECURITY: { value: SecurityLevel; label: string }[] = [
  { value: 'basic', label: 'Basic' },
  { value: 'standard', label: 'Standard' },
  { value: 'strict', label: 'Strict' },
];
const FINISH: { value: FinishSeverity; label: string }[] = [
  { value: 'critical', label: 'Critical only' },
  { value: 'high', label: 'High and above' },
  { value: 'medium', label: 'Medium and above' },
  { value: 'low', label: 'Every issue' },
];

export function ProfileSettings() {
  const { data: profile, isLoading } = useProfile();
  const main = useMainProject().data;
  return (
    <>
      <h1 className="page-title">Project profile</h1>
      <p className="muted">What this project is for. Issues are judged against it: what the analysis looks for, which issues count as work, and when the project is done.</p>
      {isLoading || !profile || !main ? <Skeleton lines={6} /> : <ProfileForm profile={profile} />}
    </>
  );
}

function ProfileForm({ profile }: { profile: ProjectProfile }) {
  const actions = useActions();
  const busy = Boolean(useBusy());
  const main = useMainProject().data;
  const [draft, setDraft] = useState<ProjectProfile>(profile);
  useEffect(() => setDraft(profile), [profile]);
  const changed = profileChanged(profile, draft) || !profile.saved;
  const set = (patch: Partial<ProjectProfile>) => setDraft(current => ({ ...current, ...patch }));
  // A new kind brings its security level, unless the level was chosen by hand.
  const setKind = (kind: ProjectKind) => set({ kind, security: draft.security === KIND_DEFAULTS[draft.kind] ? KIND_DEFAULTS[kind] : draft.security });
  const togglePlatform = (platform: ProjectPlatform, on: boolean) => set({ platforms: on ? [...draft.platforms, platform].sort() : draft.platforms.filter(p => p !== platform) });

  return (
    <section className="card" aria-label="Project profile">
      <div className="card-head">
        <Target size={15} aria-hidden />
        <span className="strong">What this project is</span>
        {!profile.saved ? <span className="xs muted">Not set yet: judged as a team project at the standard level.</span> : null}
      </div>
      <div className="card-body stack">
        <Field label="Kind">
          <Segmented label="Project kind" value={draft.kind} options={KINDS} onChange={setKind} />
        </Field>
        <Field label="Purpose" htmlFor="profile-purpose" hint="One sentence the analysis reads, such as “A command-line password manager for my own use.”">
          <Input id="profile-purpose" value={draft.purpose} maxLength={240} onChange={e => set({ purpose: e.target.value })} placeholder="What it is for, and for whom" />
        </Field>
        <Field label="Shape" htmlFor="profile-shape">
          <Select id="profile-shape" value={draft.shape} onChange={e => set({ shape: e.target.value as ProjectShape })}>
            {SHAPES.map(shape => <option key={shape.value} value={shape.value}>{shape.label}</option>)}
          </Select>
        </Field>
        <Field label="Runs on" hint="Issues in files that only build for other platforms are not counted. None checked: every platform.">
          <div className="row-wrap" role="group" aria-label="Runs on">
            {PLATFORMS.map(platform => (
              <label key={platform.value} className="check-label">
                <input type="checkbox" checked={draft.platforms.includes(platform.value)} onChange={e => togglePlatform(platform.value, e.target.checked)} />
                {platform.label}
              </label>
            ))}
          </div>
        </Field>
        <Field label="Security level" hint={SECURITY_SCOPES[draft.security](draft.shape)}>
          <Segmented label="Security level" value={draft.security} options={SECURITY} onChange={security => set({ security })} />
        </Field>
        <Field label="Finish line" htmlFor="profile-finish" hint="Issues below it stay listed under Issues → Counts, but are not work: the project is done when none at or above it is open and the tests pass.">
          <Select id="profile-finish" value={draft.finish_line} onChange={e => set({ finish_line: e.target.value as FinishSeverity })}>
            {FINISH.map(level => <option key={level.value} value={level.value}>{level.label}</option>)}
          </Select>
        </Field>
        <label className="check-label">
          <input type="checkbox" checked={draft.analyze_tests} onChange={e => set({ analyze_tests: e.target.checked })} />
          Analyze test files too
          <span className="xs muted">Off: tests are judged by running them (Health), not reviewed issue by issue.</span>
        </label>
        <Callout tone="neutral" icon={Flag}>
          Saving applies the profile to the issue list at once. The analysis reads it from the files it analyzes next; files already analyzed keep their results until they change or you re-analyze them.
        </Callout>
      </div>
      <div className="card-foot row">
        <Button size="sm" variant="ghost" onClick={() => navigate('/health')}>Open Health</Button>
        <span className="grow" />
        <Button size="sm" variant="ghost" onClick={() => setDraft(profile)} disabled={!profileChanged(profile, draft) || busy}>Discard</Button>
        <Button size="sm" variant="primary" onClick={() => main && void actions.saveProfile(main, draft)} disabled={!changed || busy || !main}>Save profile</Button>
      </div>
    </section>
  );
}
