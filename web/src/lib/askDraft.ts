// "> fix the retry loop in the llm client": a task drafted from a sentence
// typed in the command palette. It is worked out here, from the words: the
// open issue the sentence describes (its file and criteria come with it),
// the files it names, and a title. Nothing runs until the human starts it.

import type { Finding } from '@/api/types';
import { companionTestPath } from './util';

export interface RequestDraft {
  title: string;
  kind: 'fix' | 'feature';
  description: string;
  paths: string[];
  criteria: string[];
  /** The open issue the request matches, when one does. */
  issue?: Finding;
}

const STOP = new Set(['the', 'and', 'for', 'with', 'that', 'this', 'from', 'into', 'when', 'what', 'fix', 'fixes', 'make', 'please', 'should', 'does', 'not', 'its', 'are', 'was', 'add', 'can', 'it']);
const FEATURE = /^(add|show|support|allow|let|create|build|implement|introduce|new)\b/i;

/** The words that carry meaning, lowercased; "cancellation" also matches "cancel". */
export function requestWords(text: string): string[] {
  return [...new Set(text.toLowerCase().split(/[^a-z0-9_]+/).filter(word => word.length >= 3 && !STOP.has(word)))];
}

function matches(word: string, haystack: string): boolean {
  return haystack.includes(word.length > 6 ? word.slice(0, 6) : word);
}

/** The open issue most of the request's words describe; at least two must match. */
export function matchIssue(text: string, findings: Finding[]): Finding | undefined {
  const words = requestWords(text);
  let best: { finding: Finding; score: number } | undefined;
  for (const finding of findings) {
    const haystack = `${finding.title} ${finding.message} ${finding.location?.path ?? ''} ${finding.location?.symbol ?? ''}`.toLowerCase();
    const score = words.filter(word => matches(word, haystack)).length;
    if (score >= 2 && (!best || score > best.score)) best = { finding, score };
  }
  return best?.finding;
}

/** The indexed files the request names, by path or file name. */
export function namedFiles(text: string, paths: string[]): string[] {
  const tokens = text.split(/\s+/).map(token => token.replace(/^[`'"(]+|[`'",.:;)]+$/g, '')).filter(token => /[./]/.test(token));
  const found: string[] = [];
  for (const token of tokens) {
    const hit = paths.find(path => path === token || path.endsWith('/' + token));
    if (hit && !found.includes(hit)) found.push(hit);
  }
  return found;
}

export function draftFromRequest(raw: string, findings: Finding[], paths: string[]): RequestDraft {
  const text = raw.replace(/^>\s*/, '').trim();
  const issue = matchIssue(text, findings);
  const known = new Set(paths);
  const files = namedFiles(text, paths);
  if (!files.length && issue?.location?.path) files.push(issue.location.path);
  for (const file of [...files]) {
    const test = companionTestPath(file);
    if (test && known.has(test) && !files.includes(test)) files.push(test);
  }
  const sentence = text.replace(/[,;]?\s*(please\s+)?fix (it|this)\.?$/i, '').replace(/\s+/g, ' ').trim();
  const title = issue ? issue.title : sentence.charAt(0).toUpperCase() + sentence.slice(1, 100);
  return {
    title,
    kind: FEATURE.test(sentence) && !issue ? 'feature' : 'fix',
    description: text,
    paths: files.slice(0, 8),
    criteria: issue?.task_spec?.acceptance_criteria?.slice(0, 3) ?? [],
    issue,
  };
}
