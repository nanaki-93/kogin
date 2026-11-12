// Pure static-analysis helpers for the Architecture Studio: Robert C. Martin
// package coupling (Ca, Ce, Instability I = Ce / (Ca + Ce)), transitive blast
// radius, Stable Dependencies Principle (SDP) checks, and AGENTS.md boundary
// rule matching.

import type { DeclaredImportRule, DocsComponent, DocsGraph } from '@/api/types';

export type ArchitectureRoleKind = 'foundation' | 'balanced' | 'orchestrator' | 'entrypoint' | 'isolated';

export interface ComponentCoupling {
  path: string;
  ca: number;
  ce: number;
  instability: number | null;
  roleKind: ArchitectureRoleKind;
  roleLabel: string;
  transitiveCallers: string[];
  transitiveDependencies: string[];
  forbiddenImports: { never: string; source: string }[];
}

export interface StabilityInversion {
  from: string;
  to: string;
  fromInstability: number;
  toInstability: number;
}

function pathMatchesRulePrefix(componentPath: string, rulePrefix: string): boolean {
  const cleanComp = componentPath.replace(/\/+$/, '') || '.';
  const cleanRule = rulePrefix.replace(/\/+$/, '') || '.';
  if (cleanRule === '.') return true;
  return cleanComp === cleanRule || cleanComp.startsWith(cleanRule + '/') || cleanRule.startsWith(cleanComp + '/');
}

function bfsReachable(start: string, adjacency: ReadonlyMap<string, string[]>): string[] {
  const visited = new Set<string>([start]);
  const queue = [start];
  const result: string[] = [];
  while (queue.length > 0) {
    const current = queue.shift()!;
    for (const next of adjacency.get(current) ?? []) {
      if (!visited.has(next)) {
        visited.add(next);
        result.push(next);
        queue.push(next);
      }
    }
  }
  return result.sort((a, b) => a.localeCompare(b));
}

function classifyRole(
  component: DocsComponent,
  ca: number,
  ce: number,
  instability: number | null,
): { roleKind: ArchitectureRoleKind; roleLabel: string } {
  if (ca === 0 && ce === 0) {
    return component.entry_points?.length
      ? { roleKind: 'entrypoint', roleLabel: 'Entry point' }
      : { roleKind: 'isolated', roleLabel: 'Standalone' };
  }
  if (component.entry_points?.length || (ca === 0 && ce > 0)) {
    return { roleKind: 'entrypoint', roleLabel: 'Entry / leaf' };
  }
  if (ce === 0 && ca > 0) {
    return { roleKind: 'foundation', roleLabel: 'Foundational core' };
  }
  if (instability !== null && instability <= 0.35) {
    return { roleKind: 'foundation', roleLabel: 'Stable core' };
  }
  if (instability !== null && instability >= 0.7) {
    return { roleKind: 'orchestrator', roleLabel: 'Orchestrator' };
  }
  return { roleKind: 'balanced', roleLabel: 'Domain adapter' };
}

/** Computes coupling metrics, transitive reachability, and matching AGENTS.md import rules per component. */
export function analyzeArchitecture(
  components: DocsComponent[],
  graph: DocsGraph | undefined,
  declaredRules: DeclaredImportRule[] = [],
): {
  byPath: Map<string, ComponentCoupling>;
  inversions: StabilityInversion[];
} {
  const known = new Set(components.map(c => c.path));
  const importsMap = new Map<string, Set<string>>();
  const usedByMap = new Map<string, Set<string>>();
  for (const comp of components) {
    importsMap.set(comp.path, new Set((comp.imports ?? []).filter(p => p !== comp.path && known.has(p))));
    usedByMap.set(comp.path, new Set((comp.used_by ?? []).filter(p => p !== comp.path && known.has(p))));
  }
  for (const edge of graph?.edges ?? []) {
    if (edge.from !== edge.to && known.has(edge.from) && known.has(edge.to)) {
      importsMap.get(edge.from)?.add(edge.to);
      usedByMap.get(edge.to)?.add(edge.from);
    }
  }

  const forwardAdj = new Map<string, string[]>();
  const reverseAdj = new Map<string, string[]>();
  for (const [k, set] of importsMap) forwardAdj.set(k, [...set]);
  for (const [k, set] of usedByMap) reverseAdj.set(k, [...set]);

  const byPath = new Map<string, ComponentCoupling>();
  for (const comp of components) {
    const ca = usedByMap.get(comp.path)?.size ?? 0;
    const ce = importsMap.get(comp.path)?.size ?? 0;
    const total = ca + ce;
    const instability = total > 0 ? Number((ce / total).toFixed(2)) : null;
    const { roleKind, roleLabel } = classifyRole(comp, ca, ce, instability);

    const seenRules = new Set<string>();
    const forbiddenImports: { never: string; source: string }[] = [];
    for (const rule of declaredRules) {
      if (!pathMatchesRulePrefix(comp.path, rule.from)) continue;
      for (const never of rule.never) {
        const key = `${never}@${rule.source}`;
        if (seenRules.has(key)) continue;
        seenRules.add(key);
        forbiddenImports.push({ never, source: rule.source });
      }
    }

    byPath.set(comp.path, {
      path: comp.path,
      ca,
      ce,
      instability,
      roleKind,
      roleLabel,
      transitiveCallers: bfsReachable(comp.path, reverseAdj),
      transitiveDependencies: bfsReachable(comp.path, forwardAdj),
      forbiddenImports,
    });
  }

  const inversions: StabilityInversion[] = [];
  for (const [from, targets] of forwardAdj) {
    const fromMetric = byPath.get(from);
    if (!fromMetric || fromMetric.instability === null) continue;
    for (const to of targets) {
      const toMetric = byPath.get(to);
      if (!toMetric || toMetric.instability === null) continue;
      // Stable Dependencies Principle violation: a significantly more stable module imports a more volatile one.
      if (fromMetric.instability + 0.15 < toMetric.instability) {
        inversions.push({
          from,
          to,
          fromInstability: fromMetric.instability,
          toInstability: toMetric.instability,
        });
      }
    }
  }

  return { byPath, inversions };
}
