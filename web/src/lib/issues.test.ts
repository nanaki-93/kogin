import { describe, expect, it } from 'vitest';
import type { Finding } from '@/api/types';
import { groupByFile } from './issues';

const issue = (id: string, severity: string, path?: string, line?: number): Finding =>
  ({ id, project_id: 'p', source: 'ai', severity, title: id, message: '', status: 'open', location: path ? { path, start_line: line } : undefined }) as Finding;

describe('groupByFile', () => {
  it('puts the files with the worst issue first, then the busiest, and project-wide issues last', () => {
    const groups = groupByFile([
      issue('a', 'medium', 'cmd/get.go', 9),
      issue('b', 'low', 'cmd/master.go', 140),
      issue('c', 'medium', 'cmd/master.go', 26),
      issue('d', 'high', 'internal/utils/io.go', 3),
      issue('e', 'high'),
      issue('f', 'medium', 'cmd/master.go', 96),
    ]);
    expect(groups.map(g => g.path)).toEqual(['internal/utils/io.go', 'cmd/master.go', 'cmd/get.go', '']);
    expect(groups[1].findings.map(f => f.id)).toEqual(['c', 'f', 'b']);
  });
});
