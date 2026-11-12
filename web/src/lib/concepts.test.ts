import { describe, expect, it } from 'vitest';
import type { DocsComponent } from '@/api/types';
import { computeBlastRadius } from './blastRadius';
import { ATLAS_DOMAINS, allCuratedConcepts, computeAtlasMastery, conceptQuestion, lookupConcept, lookupConceptById } from './concepts';

describe('lookupConcept', () => {
  it('resolves common Go idioms and safety tags to curated micro-cards', () => {
    const wrap = lookupConcept('error wrapping');
    expect(wrap.curated).toBe(true);
    expect(wrap.category).toBe('Go idiom');
    expect(wrap.domain).toBe('Language & Verification');
    expect(wrap.title).toBe('Error wrapping (%w)');
    expect(wrap.code).toContain('%w');
    expect(wrap.mentalModel).toBeTruthy();
    expect(wrap.selfCheck?.prompt).toBeTruthy();

    const errs = lookupConcept('errors.as');
    expect(errs.curated).toBe(true);
    expect(errs.title).toBe('errors.Is & errors.As');

    const ctx = lookupConcept('context cancellation');
    expect(ctx.category).toBe('Concurrency');
    expect(ctx.domain).toBe('Concurrency & Memory');

    const table = lookupConcept('table-driven test');
    expect(table.category).toBe('Testing');
    expect(table.code).toContain('t.Run');

    const crypto = lookupConcept('constant-time compare');
    expect(crypto.category).toBe('Safety');
    expect(crypto.domain).toBe('Security & Trust');
  });

  it('covers all 7 senior CS & system design domains with unique ids and self-checks', () => {
    const all = allCuratedConcepts();
    expect(all.length).toBeGreaterThanOrEqual(30);
    const ids = new Set(all.map(c => c.id));
    expect(ids.size).toBe(all.length);
    for (const domain of ATLAS_DOMAINS) {
      const entries = all.filter(c => c.domain === domain.id);
      expect(entries.length).toBeGreaterThanOrEqual(4);
      for (const entry of entries) {
        expect(entry.selfCheck?.prompt).toBeTruthy();
        expect(entry.selfCheck?.answer).toBeTruthy();
        expect(lookupConceptById(entry.id)?.title).toBe(entry.title);
      }
    }
  });

  it('aggregates domain mastery and study-mode calibration across lessons', () => {
    const mastery = computeAtlasMastery([
      { concepts: ['error wrapping', 'table-driven test'], own_plan: 'Reject slash', own_match: 'same' },
      { concepts: ['error wrapping', 'sqlite', 'custom vault policy'], own_plan: 'Clean path', own_match: 'partly' },
      { concepts: ['tail latency'], own_plan: 'Cache everything', own_match: 'missed' },
    ]);
    expect(mastery.metCurated).toBe(4);
    expect(mastery.countsById.get('error-wrapping')).toBe(2);
    expect(mastery.countsById.get('sqlite-wal-isolation')).toBe(1);
    expect(mastery.uncategorizedTags).toEqual([{ name: 'custom vault policy', count: 1 }]);
    expect(mastery.calibration).toEqual({ written: 3, same: 1, partly: 1, missed: 1, unrated: 0 });
  });

  it('falls back cleanly for project-specific concept tags', () => {
    const custom = lookupConcept('vault unlock policy');
    expect(custom.curated).toBe(false);
    expect(custom.category).toBe('Project pattern');
    expect(custom.title).toBe('vault unlock policy');
    expect(conceptQuestion('vault unlock policy')).toContain('Explain how this project uses vault unlock policy');
  });
});

describe('computeBlastRadius', () => {
  const components: DocsComponent[] = [
    { name: 'daemon', path: 'cmd/daemon', imports: ['internal/api', 'internal/app'] },
    { name: 'api', path: 'internal/api', used_by: ['cmd/daemon'], imports: ['internal/app'] },
    { name: 'app', path: 'internal/app', used_by: ['cmd/daemon', 'internal/api'], imports: ['internal/storage'] },
    { name: 'storage', path: 'internal/storage', used_by: ['internal/app'] },
  ];

  it('maps touched files to their deepest module and separates external callers and dependencies', () => {
    const radius = computeBlastRadius(['internal/api/handlers/webui.go', 'internal/api/handlers/webui_test.go'], components);
    expect(radius).not.toBeNull();
    expect(radius!.touched.map(c => c.path)).toEqual(['internal/api']);
    expect(radius!.callers.map(c => c.path)).toEqual(['cmd/daemon']);
    expect(radius!.dependencies.map(c => c.path)).toEqual(['internal/app']);
  });

  it('excludes modules that are themselves touched from callers and dependencies', () => {
    const radius = computeBlastRadius(['internal/api/server.go', 'internal/app/service.go'], components);
    expect(radius!.touched.map(c => c.path)).toEqual(['internal/api', 'internal/app']);
    expect(radius!.callers.map(c => c.path)).toEqual(['cmd/daemon']);
    expect(radius!.dependencies.map(c => c.path)).toEqual(['internal/storage']);
  });

  it('returns null when fewer than two components exist or no paths are given', () => {
    expect(computeBlastRadius([], components)).toBeNull();
    expect(computeBlastRadius(['main.go'], [{ name: 'root', path: '.' }])).toBeNull();
  });
});
