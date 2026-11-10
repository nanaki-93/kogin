// Computes the module-level blast radius of a set of task files from the
// project's resolved component graph: which components call into the touched
// code (used_by), which components the task touches, and what they import.

import type { DocsComponent } from '@/api/types';
import { componentOf } from './docs';
import { unique } from './util';

export interface BlastRadius {
  callers: DocsComponent[];
  touched: DocsComponent[];
  dependencies: DocsComponent[];
}

export function computeBlastRadius(paths: string[], components: DocsComponent[] | undefined): BlastRadius | null {
  if (!components || components.length < 2 || !paths.length) return null;
  const byPath = new Map(components.map(component => [component.path, component]));
  const touchedIds = unique(
    paths
      .map(path => componentOf(path, components))
      .filter((id): id is string => Boolean(id && byPath.has(id))),
  );
  if (!touchedIds.length) return null;

  const touchedSet = new Set(touchedIds);
  const touched = touchedIds.map(id => byPath.get(id)!);
  const callerIds = unique(
    touched.flatMap(component => component.used_by ?? []).filter(id => !touchedSet.has(id) && byPath.has(id)),
  );
  const dependencyIds = unique(
    touched.flatMap(component => component.imports ?? []).filter(id => !touchedSet.has(id) && byPath.has(id)),
  );

  return {
    callers: callerIds.map(id => byPath.get(id)!),
    touched,
    dependencies: dependencyIds.map(id => byPath.get(id)!),
  };
}
