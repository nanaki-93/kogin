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

const ABBREVIATIONS = new Set(['e.g.', 'i.e.', 'eg.', 'ie.', 'etc.', 'vs.', 'cf.', 'approx.', 'incl.']);

/** Whether a word ending in "." is an abbreviation: a common one, or single letters with dots ("U.S."). */
function abbreviation(word: string): boolean {
  const lower = word.toLowerCase().replace(/^[(["']+/, '');
  return ABBREVIATIONS.has(lower) || lower.slice(0, -1).split('.').every(part => [...part].length === 1);
}

/** The first sentence of a model-written text, for one-line lists, cut at a word with "…" past max characters. */
export function firstSentence(text: string | undefined, max = 160): string {
  const flat = (text ?? '').replace(/\s+/g, ' ').trim();
  let end = flat.length;
  for (const match of flat.matchAll(/[.!?](?= |$)/g)) {
    const at = match.index!;
    if (match[0] === '.' && abbreviation(flat.slice(flat.lastIndexOf(' ', at) + 1, at + 1))) continue;
    end = at + 1;
    break;
  }
  const sentence = flat.slice(0, end);
  if ([...sentence].length <= max) return sentence;
  const cut = [...sentence].slice(0, max - 1).join('');
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
