import { describe, expect, it } from 'vitest';
import type { ProjectProfile } from '@/api/types';
import { KIND_DEFAULTS, SECURITY_SCOPES, finishLineWords, profileChanged } from './profile';

const saved: ProjectProfile = { kind: 'personal', purpose: 'Notes app.', shape: 'gui_backend', platforms: ['macos', 'linux'], security: 'basic', finish_line: 'medium', analyze_tests: false, plan_gate: 'always', saved: true };

describe('project profile', () => {
  it('sees a change only in fields the user edits, whatever the platform order or surrounding spaces', () => {
    expect(profileChanged(saved, { ...saved, platforms: ['linux', 'macos'], purpose: ' Notes app. ', saved: false })).toBe(false);
    expect(profileChanged(saved, { ...saved, finish_line: 'high' })).toBe(true);
    expect(profileChanged(saved, { ...saved, analyze_tests: true })).toBe(true);
  });

  it('matches the daemon: kind defaults, the frontend/backend trust of basic, and the finish line in words', () => {
    expect(KIND_DEFAULTS).toEqual({ personal: 'basic', team: 'standard', open_source: 'standard', enterprise: 'strict' });
    expect(SECURITY_SCOPES.basic('gui_backend')).toContain('frontend and backend trust each other');
    expect(SECURITY_SCOPES.basic('cli')).not.toContain('frontend');
    expect(finishLineWords('medium')).toBe('medium or above');
  });
});
