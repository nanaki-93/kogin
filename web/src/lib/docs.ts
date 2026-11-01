// Pure helpers for the Docs pages: which component a file is in, which
// walkthroughs pass through a component, and how the docs' routes read.

import type { DocsComponent, Walkthrough } from '@/api/types';
import { dirname } from './util';

/** The component holding a file: the deepest component path its directory is in. */
export function componentOf(path: string, components: Pick<DocsComponent, 'path'>[]): string | undefined {
  const dir = dirname(path) || '.';
  let best: string | undefined;
  for (const component of components) {
    const inside = component.path === '.' || dir === component.path || dir.startsWith(component.path + '/');
    if (inside && (best === undefined || best === '.' || (component.path !== '.' && component.path.length > best.length))) best = component.path;
  }
  return best;
}

/** The walkthroughs with a step in a component, with their 1-based numbers. */
export function walkthroughsThrough(componentPath: string, walkthroughs: Walkthrough[], components: Pick<DocsComponent, 'path'>[]): { n: number; walkthrough: Walkthrough }[] {
  return walkthroughs
    .map((walkthrough, index) => ({ n: index + 1, walkthrough }))
    .filter(({ walkthrough }) => walkthrough.steps.some(step => componentOf(step.path, components) === componentPath));
}

/** A component's label in lists: its path, or "Project root" for ".". */
export function componentLabel(component: Pick<DocsComponent, 'path'>): string {
  return component.path === '.' ? 'Project root' : component.path;
}

/** The first sentence of a model-written text, for one-line lists. */
export function firstSentence(text: string | undefined, max = 160): string {
  const flat = (text ?? '').replace(/\s+/g, ' ').trim();
  const end = flat.search(/[.!?](\s|$)/);
  const sentence = end >= 0 ? flat.slice(0, end + 1) : flat;
  if (sentence.length <= max) return sentence;
  const cut = sentence.slice(0, max - 1);
  const space = cut.lastIndexOf(' ');
  return (space > cut.length / 2 ? cut.slice(0, space) : cut).trimEnd() + '…';
}

/** The Docs pages; older links land on the page that replaced theirs. */
export type DocsSection = 'overview' | 'start' | 'architecture' | 'components' | 'flows' | 'dependencies' | 'ask';

const SECTIONS: Record<string, DocsSection> = {
  overview: 'overview', start: 'start', architecture: 'architecture', components: 'components', flows: 'flows', dependencies: 'dependencies', ask: 'ask',
  runbook: 'start', scenarios: 'flows', decisions: 'overview', glossary: 'overview',
};

export function docsSection(segment: string | undefined): DocsSection {
  return SECTIONS[segment ?? ''] ?? 'overview';
}
