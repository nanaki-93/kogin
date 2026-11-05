// Durations typed in words: "2m", "90s", "1h 30m", or a plain number of
// seconds, as the time limits take them.

const UNITS: Record<string, number> = { h: 3600, hr: 3600, hour: 3600, hours: 3600, m: 60, min: 60, mins: 60, minute: 60, minutes: 60, s: 1, sec: 1, secs: 1, second: 1, seconds: 1 };

/** Seconds in `text`, or NaN when it is not a duration. */
export function parseDuration(text: string): number {
  const value = text.trim().toLowerCase();
  if (!value) return Number.NaN;
  if (/^\d+$/.test(value)) return Number(value);
  let total = 0;
  let rest = value;
  const part = /^(\d+(?:\.\d+)?)\s*([a-z]+)\s*/;
  while (rest) {
    const match = part.exec(rest);
    if (!match || !UNITS[match[2]]) return Number.NaN;
    total += Number(match[1]) * UNITS[match[2]];
    rest = rest.slice(match[0].length);
  }
  return Math.round(total);
}

/** "1h 30m", "2m", "45s": a number of seconds as it is typed back. */
export function formatDurationInput(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '';
  const parts: string[] = [];
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = seconds % 60;
  if (hours) parts.push(`${hours}h`);
  if (minutes) parts.push(`${minutes}m`);
  if (rest || !parts.length) parts.push(`${rest}s`);
  return parts.join(' ');
}
