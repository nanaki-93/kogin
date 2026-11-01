import { describe, expect, it } from 'vitest';
import { reviewItemPlace, reviewItemRoute } from './workflow';

describe('review findings', () => {
  const session = { id: 'chg-1', changes: [{ path: 'cmd/master.go', content: '', hash: 'h', diff: { lines: [] } }] };

  it('reads as the file name and line', () => {
    expect(reviewItemPlace({ title: 'x', path: 'cmd/master.go', line: 42 })).toBe('master.go:42');
    expect(reviewItemPlace({ title: 'x', path: 'cmd/master.go' })).toBe('master.go');
    expect(reviewItemPlace({ title: 'x' })).toBe('');
  });

  it('leads to the diff of a changed file, else to the file in Code', () => {
    expect(reviewItemRoute(session, { title: 'x', path: 'cmd/master.go', line: 42 })).toEqual({ path: '/tasks/chg-1/files', params: { file: 'cmd/master.go', line: '42' } });
    expect(reviewItemRoute(session, { title: 'x', path: 'cmd/add.go' })).toEqual({ path: '/code', params: { file: 'cmd/add.go', line: undefined } });
    expect(reviewItemRoute(session, { title: 'x' })).toBeNull();
  });
});
