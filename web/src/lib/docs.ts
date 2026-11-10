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

/** The architecture read by component: what each does, and the lines that name no component. */
export interface ArchitectureParts {
  roles: Map<string, string>;
  rest: string[];
}

/**
 * What each component does, in the architecture's own words: a "path: role"
 * line names a component, or a file in one (the first such line wins). A
 * line that names neither stays as it is, in `rest`.
 */
export function architectureParts(text: string | undefined, components: Pick<DocsComponent, 'path'>[]): ArchitectureParts {
  const paths = new Set(components.map(component => component.path));
  const roles = new Map<string, string>();
  const rest: string[] = [];
  for (const line of architectureLines(text)) {
    const colon = line.indexOf(': ');
    const name = colon < 1 ? '' : line.slice(0, colon).replace(/`/g, '').replace(/^\.\//, '').replace(/\/$/, '').trim();
    const role = colon < 1 ? '' : line.slice(colon + 2).trim();
    const path = !name ? undefined : paths.has(name) ? name : /\.[A-Za-z0-9]+$/.test(name) ? componentOf(name, components) : undefined;
    if (path !== undefined && role && !roles.has(path)) roles.set(path, role);
    else rest.push(line);
  }
  return { roles, rest };
}

export function architectureRoles(text: string | undefined, components: Pick<DocsComponent, 'path'>[]): Map<string, string> {
  return architectureParts(text, components).roles;
}

/** The components a guide's steps pass through, each once, in the order the steps first reach them. */
export function componentTrail(guide: Walkthrough, components: Pick<DocsComponent, 'path'>[]): string[] {
  const trail: string[] = [];
  for (const step of guide.steps) {
    const component = componentOf(step.path, components);
    if (component !== undefined && !trail.includes(component)) trail.push(component);
  }
  return trail;
}

/** A declaration the guides stop at inside a component, with what a step says of it and the guides that pass it. */
export interface GuideStop {
  symbol: string;
  path: string;
  line: number;
  text: string;
  flows: number[];
  changes: number[];
}

/** The declarations in a component that walkthroughs and the change guide stop at, most visited first. */
export function guideStops(componentPath: string, walkthroughs: Walkthrough[], changes: Walkthrough[], components: Pick<DocsComponent, 'path'>[]): GuideStop[] {
  const stops = new Map<string, GuideStop>();
  const collect = (guides: Walkthrough[], key: 'flows' | 'changes') => guides.forEach((guide, index) => {
    for (const step of guide.steps) {
      if (!step.symbol || !step.line || componentOf(step.path, components) !== componentPath) continue;
      const id = `${step.path}#${step.symbol}`;
      const stop = stops.get(id) ?? { symbol: step.symbol, path: step.path, line: step.line, text: step.text, flows: [], changes: [] };
      if (!stop[key].includes(index + 1)) stop[key].push(index + 1);
      stops.set(id, stop);
    }
  });
  collect(walkthroughs, 'flows');
  collect(changes, 'changes');
  const visits = (stop: GuideStop) => stop.flows.length + stop.changes.length;
  return [...stops.values()].sort((a, b) => visits(b) - visits(a) || a.path.localeCompare(b.path) || a.line - b.line);
}

/** The lines of a declaration, from the line a step found it at. */
export interface Excerpt {
  first: number;
  lines: string[];
  /** Lines of the declaration left out, past `max`. */
  more: number;
}

/**
 * A declaration's source from its first line: to the line that closes it at
 * the same indentation (a brace language), or to the last line indented
 * under it (Python and the like); at most `max` lines, without the
 * declaration's own indentation.
 */
export function declarationExcerpt(content: string, line: number, max = 40): Excerpt | null {
  const all = content.split('\n');
  if (line < 1 || line > all.length || !all[line - 1].trim()) return null;
  const indent = (text: string) => text.length - text.trimStart().length;
  const base = indent(all[line - 1]);
  let end = line;
  for (let at = line; at < all.length; at++) {
    const text = all[at];
    if (!text.trim()) continue;
    if (indent(text) > base) { end = at + 1; continue; }
    if (indent(text) !== base || !/^[\])}]/.test(text.trimStart())) break;
    end = at + 1;
    // ") error {" closes a signature and opens the body: the declaration goes on.
    if (!/[{(\[]$/.test(text.trimEnd())) break;
  }
  const lines = all.slice(line - 1, Math.min(end, line - 1 + max)).map(text => text.slice(Math.min(base, indent(text))));
  while (lines.length > 1 && !lines[lines.length - 1].trim()) lines.pop();
  return { first: line, lines, more: Math.max(0, end - (line - 1) - lines.length) };
}

/** The Docs pages; older links land on the page that replaced theirs. */
export type DocsSection = 'overview' | 'start' | 'architecture' | 'map' | 'components' | 'flows' | 'changes' | 'dependencies' | 'ask' | 'log' | 'decisions';

const SECTIONS: Record<string, DocsSection> = {
  overview: 'overview', start: 'start', architecture: 'architecture', map: 'map', components: 'components', flows: 'flows', changes: 'changes', dependencies: 'dependencies', ask: 'ask', log: 'log',
  runbook: 'start', scenarios: 'flows', decisions: 'decisions', glossary: 'overview',
};

export function docsSection(segment: string | undefined): DocsSection {
  return SECTIONS[segment ?? ''] ?? 'overview';
}
