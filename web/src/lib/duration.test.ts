import { describe, expect, it } from 'vitest';
import { formatDurationInput, parseDuration } from './duration';

describe('durations in words', () => {
  it('reads minutes, hours, seconds and plain numbers', () => {
    expect(parseDuration('2m')).toBe(120);
    expect(parseDuration('1h 30m')).toBe(5400);
    expect(parseDuration('90s')).toBe(90);
    expect(parseDuration('15 min')).toBe(900);
    expect(parseDuration('300')).toBe(300);
    expect(parseDuration('soon')).toBeNaN();
    expect(parseDuration('')).toBeNaN();
  });

  it('writes them back the short way', () => {
    expect(formatDurationInput(120)).toBe('2m');
    expect(formatDurationInput(5400)).toBe('1h 30m');
    expect(formatDurationInput(90)).toBe('1m 30s');
    expect(formatDurationInput(45)).toBe('45s');
  });
});
