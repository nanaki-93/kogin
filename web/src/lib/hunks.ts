// Hunk-by-hunk review: a file's diff split into the hunks a reviewer reads
// one at a time, and the hunks the reviewer already called right. When the
// walkthrough ordered the hunks (cause → fix → support → test), the guided
// order replaces file order.

import type { ChangePlan, ChangeWalkthrough, ChangeWorkflow, DiffLine, FileChange, ReviewItem, WalkthroughNote } from '@/api/types';
import { buildSegments, isChange, lineAnchor, noteStart, type LineAnchor } from './diff';

export interface Hunk {
  path: string;
  /** Its place among the file's hunks, from 0. */
  n: number;
  /** The slice of the file's diff lines it covers, with context. */
  start: number;
  end: number;
}

/** The hunks of a file's diff: each run of changes with three lines of context, as the full diff shows them. */
export function fileHunks(path: string, lines: DiffLine[]): Hunk[] {
  return buildSegments(lines, 3)
    .filter(segment => segment.type === 'lines' && lines.slice(segment.start, segment.end).some(isChange))
    .map((segment, n) => ({ path, n, start: segment.start, end: segment.end }));
}

/** Every hunk of the edits, file by file. */
export function reviewHunks(edits: FileChange[]): Hunk[] {
  return edits.flatMap(edit => fileHunks(edit.path, edit.diff?.lines ?? []));
}

/** Where a comment on the hunk goes: its first changed line. */
export function hunkAnchor(lines: DiffLine[], hunk: Hunk): LineAnchor | null {
  for (let index = hunk.start; index < hunk.end; index++) {
    if (isChange(lines[index])) return lineAnchor(lines[index]);
  }
  return null;
}

/** Whether the hunk shows `line` of the new file. */
export function hunkHasLine(lines: DiffLine[], hunk: Hunk, line: number): boolean {
  return lines.slice(hunk.start, hunk.end).some(entry => entry.kind !== 'removed' && entry.new_line === line);
}

export const hunkKey = (hunk: Pick<Hunk, 'path' | 'n'>) => `${hunk.path}#${hunk.n}`;

/** The hunks in the walkthrough's guided order, when it ordered them. */
export interface GuidedHunks {
  hunks: Hunk[];
  /** Whether the walkthrough's causal order replaced file order. */
  guided: boolean;
  /** Each hunk's part in the story (cause, fix, support or test), by hunk key. */
  kinds: Map<string, string>;
}

/**
 * Orders the hunks the way the walkthrough tells the change's story: the
 * problem's cause and fix first, then supporting changes, then the tests.
 * Hunks its notes did not order keep their file order after the ordered
 * ones. Without a current walkthrough with steps, file order stands.
 */
export function guidedHunks(session: { walkthrough?: ChangeWalkthrough; hash: string }, linesOf: Map<string, DiffLine[]>, all: Hunk[]): GuidedHunks {
  const walkthrough = session.walkthrough;
  const kinds = new Map<string, string>();
  if (walkthrough?.status !== 'ready' || walkthrough.proposal_hash !== session.hash) return { hunks: all, guided: false, kinds };
  const steps = new Map<string, number>();
  for (const file of walkthrough.files ?? []) {
    const lines = linesOf.get(file.path) ?? [];
    const hunks = all.filter(hunk => hunk.path === file.path);
    for (const note of file.notes ?? []) {
      const at = note.line ? noteStart(lines, note.line) : -1;
      const hunk = at >= 0 ? hunks.find(entry => at >= entry.start && at < entry.end) : undefined;
      if (!hunk) continue;
      const key = hunkKey(hunk);
      if (note.kind && !kinds.has(key)) kinds.set(key, note.kind);
      if (note.step && note.step < (steps.get(key) ?? Infinity)) steps.set(key, note.step);
    }
  }
  if (!steps.size) return { hunks: all, guided: false, kinds };
  const hunks = [...all].sort((a, b) => (steps.get(hunkKey(a)) ?? Infinity) - (steps.get(hunkKey(b)) ?? Infinity));
  return { hunks, guided: true, kinds };
}

/** How a hunk's part in the story reads on its chip. */
export const HUNK_KINDS: Record<string, { label: string; tone: 'danger' | 'success' | 'neutral' | 'info' }> = {
  cause: { label: 'Cause', tone: 'danger' },
  fix: { label: 'Fix', tone: 'success' },
  support: { label: 'Supports the fix', tone: 'neutral' },
  test: { label: 'Proof', tone: 'info' },
};

/** One hunk as the review reads it: its notes and where it stands against the approved plan. */
export interface ReviewBlock {
  hunk: Hunk;
  notes: WalkthroughNote[];
  /** The plan step it carries out; 0 when none. */
  planStep: number;
  /** Its part in the story, when the walkthrough said. */
  kind?: string;
  /** Needs your eyes first: outside the approved plan, or a hunk nobody explained. */
  exception?: 'outside' | 'unexplained';
}

/** The change set against the plan you approved. */
export interface PlanResult {
  /** Whether the walkthrough set the hunks against the task's approved plan. */
  mapped: boolean;
  /** The approved steps, each with the hunks that carry it out. */
  steps: { n: number; text: string; blocks: ReviewBlock[] }[];
  /** Every hunk in reading order: exceptions, then the plan's steps, then the rest. */
  blocks: ReviewBlock[];
}

/** An approach's steps without their numbers. */
function planSteps(plan: Pick<ChangePlan, 'approach'> | undefined): string[] {
  return (plan?.approach ?? '').split('\n').map(line => line.replace(/^\s*\d+[.)]\s*/, '').trim()).filter(Boolean);
}

/**
 * Sets the change against the approved plan. With a current walkthrough
 * written against that plan, each hunk is a plan step's, proof (a test), or
 * an exception: outside the plan, or not explained at all. Exceptions are
 * read first, then the steps in the plan's order. Without such a walkthrough
 * the hunks keep the guided (or file) order and nothing is an exception.
 */
export function planResult(session: { walkthrough?: ChangeWalkthrough; hash: string; plan?: ChangePlan }, linesOf: Map<string, DiffLine[]>, all: Hunk[]): PlanResult {
  const walkthrough = session.walkthrough;
  const current = walkthrough?.status === 'ready' && walkthrough.proposal_hash === session.hash;
  const plan = session.plan;
  const mapped = Boolean(current && walkthrough?.plan_hash && plan?.source === 'investigator' && plan.approved_hash === walkthrough.plan_hash);
  // A hunk that several notes explain is read as one block per note.
  const split = current ? splitAtNotes(all, linesOf, walkthrough!) : all;
  const guided = guidedHunks(session, linesOf, split);
  const blocks = guided.hunks.map((hunk): ReviewBlock => {
    const lines = linesOf.get(hunk.path) ?? [];
    const file = current ? walkthrough?.files?.find(entry => entry.path === hunk.path) : undefined;
    const notes = (file?.notes ?? []).filter(note => {
      const at = note.line ? noteStart(lines, note.line) : -1;
      return at >= hunk.start && at < hunk.end;
    });
    const planStep = notes.find(note => note.plan_step)?.plan_step ?? 0;
    const kind = guided.kinds.get(hunkKey(hunk));
    const proof = notes.some(note => note.kind === 'test');
    const exception = !mapped ? undefined : !notes.length ? 'unexplained' : !planStep && !proof ? 'outside' : undefined;
    return { hunk, notes, planStep, kind, exception };
  });
  const rank = (block: ReviewBlock) => (block.exception ? 0 : block.planStep || 99);
  const ordered = mapped ? blocks.map((block, index) => ({ block, index })).sort((a, b) => rank(a.block) - rank(b.block) || a.index - b.index).map(entry => entry.block) : blocks;
  const steps = mapped ? planSteps(plan).map((text, index) => ({ n: index + 1, text, blocks: ordered.filter(block => block.planStep === index + 1) })) : [];
  return { mapped, steps, blocks: ordered };
}

/**
 * Splits the hunks several notes explain so each block is one note's change:
 * the runs of changed lines of a hunk are grouped from each run a note
 * starts at, and the unchanged lines between two groups are shared out.
 * A file's blocks are numbered again from 0.
 */
export function splitAtNotes(all: Hunk[], linesOf: Map<string, DiffLine[]>, walkthrough: ChangeWalkthrough): Hunk[] {
  const out: Hunk[] = [];
  const counts = new Map<string, number>();
  for (const hunk of all) {
    const lines = linesOf.get(hunk.path) ?? [];
    const notes = (walkthrough.files?.find(file => file.path === hunk.path)?.notes ?? []).map(note => (note.line ? noteStart(lines, note.line) : -1)).filter(at => at >= hunk.start && at < hunk.end);
    for (const [start, end] of noteGroups(hunk, lines, notes)) {
      const n = counts.get(hunk.path) ?? 0;
      counts.set(hunk.path, n + 1);
      out.push({ path: hunk.path, n, start, end });
    }
  }
  return out;
}

/** The ranges of a hunk that each hold the changes one note is about. */
function noteGroups(hunk: Hunk, lines: DiffLine[], notes: number[]): [number, number][] {
  const runs: [number, number][] = [];
  for (let index = hunk.start; index < hunk.end; index++) {
    if (!isChange(lines[index])) continue;
    const last = runs.at(-1);
    if (last && last[1] === index) last[1] = index + 1;
    else runs.push([index, index + 1]);
  }
  // A note belongs to the run it points into, or the next one after it.
  const noted = new Set(notes.map(at => runs.findIndex(run => at < run[1])).filter(run => run >= 0));
  const groups: [number, number][] = [];
  runs.forEach((run, index) => {
    if (index === 0 || noted.has(index)) groups.push([run[0], run[1]]);
    else groups.at(-1)![1] = run[1];
  });
  if (groups.length < 2) return [[hunk.start, hunk.end]];
  return groups.map((group, index) => {
    const before = index === 0 ? hunk.start : Math.ceil((groups[index - 1][1] + group[0]) / 2);
    const after = index === groups.length - 1 ? hunk.end : Math.ceil((group[1] + groups[index + 1][0]) / 2);
    return [before, after];
  });
}

/** A reviewer suggestion with everyone who made it. */
export interface MergedNote {
  item: ReviewItem;
  reviewers: string[];
}

/**
 * The reviewers' advisory notes on this version, said once: notes on the
 * same line of the same file (or, without a line, with the same title) are
 * one suggestion, with the reviewers who made it.
 */
export function mergedReviewNotes(workflow: ChangeWorkflow | undefined, hash: string): MergedNote[] {
  const merged = new Map<string, MergedNote>();
  for (const specialist of workflow?.specialists ?? []) {
    const review = specialist.review;
    if (!review || (review.proposal_hash && review.proposal_hash !== hash)) continue;
    for (const item of review.notes ?? []) {
      const key = item.path && item.line ? `${item.path}:${item.line}` : item.title.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim();
      const seen = merged.get(key);
      if (seen) {
        if (!seen.reviewers.includes(specialist.id)) seen.reviewers.push(specialist.id);
      } else merged.set(key, { item, reviewers: [specialist.id] });
    }
  }
  if (!merged.size && workflow?.review?.proposal_hash === hash) return (workflow.review.notes ?? []).map(item => ({ item, reviewers: [] }));
  return [...merged.values()];
}

/**
 * The hunks the reviewer called right, per task version, kept in this
 * browser: a new version starts over, like the files viewed.
 */
export function loadVerdicts(id: string, hash: string): Set<string> {
  try {
    const raw = localStorage.getItem(verdictKey(id, hash));
    const list: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(list) ? list.filter((item): item is string => typeof item === 'string') : []);
  } catch {
    return new Set();
  }
}

export function saveVerdicts(id: string, hash: string, verdicts: Set<string>): void {
  try {
    localStorage.setItem(verdictKey(id, hash), JSON.stringify([...verdicts]));
  } catch {
    // A browser without storage keeps the verdicts for this visit only.
  }
}

const verdictKey = (id: string, hash: string) => `kogin.hunks.${id}.${hash}`;
