import { describe, expect, it } from 'vitest';
import type { DocsComponent } from '@/api/types';
import { analyzeArchitecture } from './architectureLens';

describe('analyzeArchitecture', () => {
  const components: DocsComponent[] = [
    { name: 'Daemon', path: 'cmd/daemon', entry_points: [{ path: 'cmd/daemon/main.go', kind: 'main' }], imports: ['internal/api', 'internal/app'], used_by: [] },
    { name: 'API', path: 'internal/api', imports: ['internal/app'], used_by: ['cmd/daemon'] },
    { name: 'App', path: 'internal/app', imports: ['internal/project'], used_by: ['cmd/daemon', 'internal/api'] },
    { name: 'Project', path: 'internal/project', imports: [], used_by: ['internal/app'] },
    { name: 'Web', path: 'web/src', imports: [], used_by: [] },
  ];

  it('computes Ca, Ce, Martin instability I, and transitive reachability accurately', () => {
    const { byPath, inversions } = analyzeArchitecture(components, undefined, [
      { from: 'internal/app', never: ['internal/api'], source: 'AGENTS.md' },
      { from: 'internal/project/diff', never: ['internal/llm'], source: 'AGENTS.md' },
    ]);

    const daemon = byPath.get('cmd/daemon')!;
    expect(daemon.ca).toBe(0);
    expect(daemon.ce).toBe(2);
    expect(daemon.instability).toBe(1);
    expect(daemon.roleKind).toBe('entrypoint');
    expect(daemon.transitiveDependencies).toEqual(['internal/api', 'internal/app', 'internal/project']);

    const app = byPath.get('internal/app')!;
    expect(app.ca).toBe(2);
    expect(app.ce).toBe(1);
    expect(app.instability).toBe(0.33);
    expect(app.roleKind).toBe('foundation');
    expect(app.forbiddenImports).toEqual([{ never: 'internal/api', source: 'AGENTS.md' }]);

    const project = byPath.get('internal/project')!;
    expect(project.ca).toBe(1);
    expect(project.ce).toBe(0);
    expect(project.instability).toBe(0);
    expect(project.roleLabel).toBe('Foundational core');
    expect(project.transitiveCallers).toEqual(['cmd/daemon', 'internal/api', 'internal/app']);
    expect(project.forbiddenImports).toEqual([{ never: 'internal/llm', source: 'AGENTS.md' }]);

    const web = byPath.get('web/src')!;
    expect(web.instability).toBeNull();
    expect(web.roleKind).toBe('isolated');

    expect(inversions).toEqual([]);
  });

  it('flags Stable Dependencies Principle inversions when a stable module imports a volatile one', () => {
    const inverted: DocsComponent[] = [
      { name: 'CallerA', path: 'pkg/a', imports: ['pkg/core'], used_by: [] },
      { name: 'CallerB', path: 'pkg/b', imports: ['pkg/core'], used_by: [] },
      { name: 'Core', path: 'pkg/core', imports: ['pkg/volatile'], used_by: ['pkg/a', 'pkg/b'] },
      { name: 'Volatile', path: 'pkg/volatile', imports: ['pkg/leaf1', 'pkg/leaf2'], used_by: ['pkg/core'] },
      { name: 'Leaf1', path: 'pkg/leaf1', imports: [], used_by: ['pkg/volatile'] },
      { name: 'Leaf2', path: 'pkg/leaf2', imports: [], used_by: ['pkg/volatile'] },
    ];
    const { inversions } = analyzeArchitecture(inverted, undefined);
    expect(inversions).toHaveLength(1);
    expect(inversions[0]).toMatchObject({ from: 'pkg/core', to: 'pkg/volatile' });
  });
});
