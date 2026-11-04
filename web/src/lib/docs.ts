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

/** The first sentence of a model-written text, for one-line lists (backticks dropped), cut at a word with "…" past max characters. */
export function firstSentence(text: string | undefined, max = 160): string {
  const flat = (text ?? '').replace(/`/g, '').replace(/\s+/g, ' ').trim();
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

/** What docs prose can link to: the project's files, its components and the declarations the overview's steps found. */
export interface DocRefs {
  files: ReadonlySet<string>;
  components: ReadonlySet<string>;
  symbols: ReadonlyMap<string, { path: string; line?: number }>;
}

/** A piece of docs prose: plain text, inline code, or a link to a file, component or declaration. */
export type DocTextPart =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'file'; text: string; path: string; line?: number }
  | { kind: 'component'; text: string; path: string }
  | { kind: 'symbol'; text: string; path: string; line?: number };

/**
 * The declarations the walkthrough and change-guide steps found, by name and,
 * when only one declaration has it, by its last part ("Save" for "Store.Save").
 */
export function symbolRefs(guides: Walkthrough[]): Map<string, { path: string; line?: number }> {
  const refs = new Map<string, { path: string; line?: number }>();
  const short = new Map<string, { path: string; line?: number } | null>();
  for (const step of guides.flatMap(guide => guide.steps)) {
    if (!step.symbol || !step.line) continue;
    const place = { path: step.path, line: step.line };
    refs.set(step.symbol, place);
    const last = step.symbol.slice(step.symbol.lastIndexOf('.') + 1);
    const seen = short.get(last);
    short.set(last, seen === undefined || (seen && seen.path === place.path && seen.line === place.line) ? place : null);
  }
  for (const [name, place] of short) if (place && !refs.has(name)) refs.set(name, place);
  return refs;
}

// A file path in prose: a word with a slash or an extension, such as cmd/add.go.
const PATH_WORD = /[A-Za-z0-9_][A-Za-z0-9_.\-/]*/g;

/** Reads a `code` span: the file, component or declaration it names, else inline code. */
function codePart(raw: string, refs: DocRefs): DocTextPart {
  const [path, line] = splitLine(raw.replace(/^\.\//, '').replace(/\/$/, ''));
  if (refs.files.has(path)) return { kind: 'file', text: raw, path, line };
  if (refs.components.has(path)) return { kind: 'component', text: raw, path };
  const name = raw.replace(/\(\)$/, '').replace(/^\(\*?([^)]+)\)/, '$1');
  const symbol = refs.symbols.get(name);
  return symbol ? { kind: 'symbol', text: raw, ...symbol } : { kind: 'code', text: raw };
}

/** "cmd/add.go:42" → ["cmd/add.go", 42]. */
function splitLine(text: string): [string, number | undefined] {
  const match = text.match(/^(.*):(\d+)$/);
  return match ? [match[1], Number(match[2])] : [text, undefined];
}

/** Links the file and component paths in plain prose; a component only by a path with a slash, so "cmd" in a sentence stays a word. */
function plainParts(text: string, refs: DocRefs): DocTextPart[] {
  const parts: DocTextPart[] = [];
  let at = 0;
  for (const match of text.matchAll(PATH_WORD)) {
    const word = match[0].replace(/[.\-]+$/, '');
    if (!word.includes('/') && !word.includes('.')) continue;
    const linked = refs.files.has(word) ? { kind: 'file' as const, text: word, path: word }
      : word.includes('/') && refs.components.has(word.replace(/\/$/, '')) ? { kind: 'component' as const, text: word, path: word.replace(/\/$/, '') } : null;
    if (!linked) continue;
    if (match.index! > at) parts.push({ kind: 'text', text: text.slice(at, match.index) });
    parts.push(linked);
    at = match.index! + word.length;
  }
  if (at < text.length) parts.push({ kind: 'text', text: text.slice(at) });
  return parts;
}

/** Splits model-written prose into text and links: `backticked` names and the project's file paths point at the code. */
export function docTextParts(text: string, refs: DocRefs): DocTextPart[] {
  const parts: DocTextPart[] = [];
  text.split('`').forEach((piece, index, pieces) => {
    const inCode = index % 2 === 1 && index < pieces.length - 1;
    if (inCode) parts.push(codePart(piece, refs));
    else if (piece) parts.push(...plainParts(index % 2 === 1 ? '`' + piece : piece, refs));
  });
  return parts;
}

/** Architecture as the overview writes it since prompt v8: one "path: role" line each; older overviews are one paragraph. */
export function architectureLines(text: string | undefined): string[] {
  return (text ?? '').split('\n').map(line => line.replace(/^\s*[-*•]\s*/, '').trim()).filter(Boolean);
}

/** The Docs pages; older links land on the page that replaced theirs. */
export type DocsSection = 'overview' | 'start' | 'architecture' | 'components' | 'flows' | 'changes' | 'dependencies' | 'ask';

const SECTIONS: Record<string, DocsSection> = {
  overview: 'overview', start: 'start', architecture: 'architecture', components: 'components', flows: 'flows', changes: 'changes', dependencies: 'dependencies', ask: 'ask',
  runbook: 'start', scenarios: 'flows', decisions: 'overview', glossary: 'overview',
};

export function docsSection(segment: string | undefined): DocsSection {
  return SECTIONS[segment ?? ''] ?? 'overview';
}
