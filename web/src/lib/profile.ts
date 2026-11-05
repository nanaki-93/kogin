// The project profile's defaults and wording, as the daemon applies them
// (internal/app/project_profile.go).

import type { ProjectKind, ProjectProfile, ProjectShape, SecurityLevel } from '@/api/types';

/** The security level each kind starts with. */
export const KIND_DEFAULTS: Record<ProjectKind, SecurityLevel> = { personal: 'basic', team: 'standard', open_source: 'standard', enterprise: 'strict' };

/** What each security level puts in scope, for the project's shape. */
export const SECURITY_SCOPES: Record<SecurityLevel, (shape: ProjectShape) => string> = {
  basic: shape =>
    'You, your machine and the config you wrote are trusted. In scope: input from outside your control, losing or leaking your data. Out: attackers already running code as you, wiping secrets from memory.' +
    (shape === 'gui_backend' ? ' Its own frontend and backend trust each other.' : ''),
  standard: shape =>
    'In scope: outside input, other local users, file permissions and network-facing APIs. Out: attackers already running code as you, wiping secrets from memory.' +
    (shape === 'gui_backend' ? ' Frontend and backend trust each other while the backend listens only on this machine.' : ''),
  strict: () => 'Everything: other users and processes, every boundary between components, secrets in memory, audit and defense in depth.',
};

/** Whether a draft differs from the saved profile in a field the user edits. */
export function profileChanged(saved: ProjectProfile, draft: ProjectProfile): boolean {
  return (
    saved.kind !== draft.kind ||
    saved.purpose.trim() !== draft.purpose.trim() ||
    saved.shape !== draft.shape ||
    [...saved.platforms].sort().join() !== [...draft.platforms].sort().join() ||
    saved.security !== draft.security ||
    saved.finish_line !== draft.finish_line ||
    saved.analyze_tests !== draft.analyze_tests
  );
}

const FINISH_WORDS: Record<string, string> = { critical: 'critical', high: 'high or critical', medium: 'medium or above', low: 'any severity' };

/** The finish line in words: "medium or above". */
export function finishLineWords(severity: string): string {
  return FINISH_WORDS[severity] ?? severity;
}
