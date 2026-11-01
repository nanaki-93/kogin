// Small pure helpers shared across views.

export function plural(count: number, singular: string, pluralForm?: string): string {
  return `${count} ${count === 1 ? singular : pluralForm ?? singular + 's'}`;
}

export function basename(path: string): string {
  const index = path.lastIndexOf('/');
  return index >= 0 ? path.slice(index + 1) : path;
}

export function dirname(path: string): string {
  const index = path.lastIndexOf('/');
  return index > 0 ? path.slice(0, index) : '';
}

export function shortHash(value: string | undefined, length = 7): string {
  return value ? value.replace(/^[a-z0-9]+:/i, '').slice(0, length) : '';
}

export function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export function unique<T>(values: T[]): T[] {
  return [...new Set(values.filter(Boolean))];
}

/** Formats a past timestamp as a compact relative label ("3m ago"). */
export function timeAgo(value: string | number | undefined, now = Date.now()): string {
  const time = typeof value === 'number' ? value : Date.parse(value ?? '');
  if (!Number.isFinite(time) || time <= 0) return '';
  const seconds = Math.max(0, Math.round((now - time) / 1000));
  if (seconds < 45) return 'just now';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return days < 30 ? `${days}d ago` : new Date(time).toISOString().slice(0, 10);
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '';
  if (ms < 1000) return `${ms} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)} s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.round(seconds % 60)}s`;
}

/** Formats a running time as m:ss ("0:07", "2:05"). */
export function formatElapsed(ms: number): string {
  const seconds = Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0;
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

export function durationBetween(start?: string, end?: string): number {
  const a = Date.parse(start ?? '');
  const b = Date.parse(end ?? '');
  return Number.isFinite(a) && Number.isFinite(b) && b >= a ? b - a : 0;
}

export { companionTestPath, isTestPath } from './rules';

/**
 * Returns the project-relative path a typed query names as a new file
 * ("./cmd/util_test.go" is cmd/util_test.go), or '' when it cannot name one:
 * absolute, escaping, empty or dot segments, a trailing slash, control
 * characters, or a last segment without an extension. The daemon still applies
 * the write and context policy when the task is created.
 */
export function newFilePath(query: string): string {
  const path = query.trim().replace(/^(\.\/)+/, '');
  if (!path || path.length > 512 || path.startsWith('/') || path.includes('\\') || /[\u0000-\u001f\u007f]/.test(path)) return '';
  const segments = path.split('/');
  if (segments.some(segment => segment === '' || segment === '.' || segment === '..')) return '';
  return /^[^.].*\.[^.]+$/.test(segments[segments.length - 1]) ? path : '';
}

const SEVERITY_RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };

export function severityRank(severity: string | undefined): number {
  return SEVERITY_RANK[(severity ?? '').toLowerCase()] ?? 5;
}

export type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger' | 'accent';

export function severityTone(severity: string | undefined): Tone {
  const rank = severityRank(severity);
  return rank <= 1 ? 'danger' : rank === 2 ? 'warning' : 'neutral';
}

export function languageOf(path: string): string {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
  const map: Record<string, string> = {
    go: 'go', ts: 'typescript', tsx: 'typescript', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
    json: 'json', yaml: 'yaml', yml: 'yaml', md: 'markdown', py: 'python', sh: 'bash', bash: 'bash', css: 'css',
    html: 'xml', xml: 'xml', toml: 'ini', mod: 'go', sql: 'sql', rs: 'rust', java: 'java', kt: 'kotlin', rb: 'ruby',
  };
  if (basename(path) === 'Makefile') return 'makefile';
  if (basename(path) === 'Dockerfile') return 'dockerfile';
  return map[ext] ?? 'plaintext';
}

export function clamp(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value));
}

/** How open line comments travel with a run that writes code. */
export function commentsBrief(count: number): string {
  return `Your ${plural(count, 'line comment')} ${count === 1 ? 'goes' : 'go'} to the agents as a repair brief.`;
}
