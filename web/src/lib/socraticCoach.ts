// Deterministic Socratic prompts for Study-mode plan formulation and
// Senior Systems review lenses on diff hunks. Pure static heuristics; never
// calls a model.

import type { DiffLine } from '@/api/types';
import { isTestPath } from './util';

export interface SocraticPlanLens {
  lens: string;
  prompts: string[];
  conceptId: string;
  conceptLabel: string;
}

export interface SeniorHunkCheck {
  label: string;
  question: string;
  conceptId: string;
}

/** Tailors first-principles thinking questions before revealing the agent's plan. */
export function socraticPlanPrompts(kind: string, targetPaths: string[]): SocraticPlanLens {
  const joined = targetPaths.join(' ').toLowerCase();
  if (kind === 'security' || /(auth|token|secret|sandbox|trust|crypto|handler)/.test(joined) && kind === 'security') {
    return {
      lens: 'Trust boundary & canonicalization',
      prompts: [
        'Where is the exact trust boundary, and can untrusted input bypass it via symlinks, "../", or sibling prefixes?',
        'Are request sizes, unknown fields, and error leaks constrained at the edge before domain logic runs?',
        'What minimal adversarial test proves the exploit path fails closed?',
      ],
      conceptId: 'boundary-parse-dont-validate',
      conceptLabel: 'Parse, don’t validate',
    };
  }
  if (kind === 'performance' || /(cache|index|graph|dupes|stream)/.test(joined) && kind === 'performance') {
    return {
      lens: 'Complexity, allocations & invalidation',
      prompts: [
        'What scales super-linearly (O(N²), repeated scans, per-item allocations) when repository size grows 100×?',
        'If introducing caching or indexing, what content digest or revision invalidates stale entries?',
        'Could concurrency or fan-out introduce lock contention or p99 tail latency amplification?',
      ],
      conceptId: 'amortized-complexity',
      conceptLabel: 'Amortized complexity',
    };
  }
  if (kind === 'fix') {
    return {
      lens: 'Broken invariant & blast radius',
      prompts: [
        'Which invariant broke—not just the visible symptom—and what is the earliest boundary that should enforce it?',
        'Which callers or clients depend on the current status code, error chain, or ordering?',
        'What single deterministic test fails on current main and passes once the invariant holds?',
      ],
      conceptId: 'state-machines-illegal-states',
      conceptLabel: 'Invariants & state',
    };
  }
  return {
    lens: 'State ownership & package boundaries',
    prompts: [
      'Who owns the state lifecycle, and can illegal state combinations be made unrepresentable in types?',
      'Do package imports point strictly toward more stable domain leaves (lower Martin Instability I)?',
      'How does context cancellation, partial failure, or a retried request behave mid-flight?',
    ],
    conceptId: 'ports-adapters-leaf-packages',
    conceptLabel: 'Hexagonal boundaries',
  };
}

const HUNK_RULES: { test: RegExp; check: SeniorHunkCheck }[] = [
  {
    test: /\b(go\s+[a-zA-Z_]|sync\.(Mutex|RWMutex|WaitGroup)|errgroup|make\(\s*chan\b|<-ctx\.Done)/,
    check: {
      label: 'Concurrency & lifecycle',
      question: 'Who joins this goroutine or unlocks this mutex on early return, error, or context cancellation?',
      conceptId: 'goroutine-lifecycle',
    },
  },
  {
    test: /\b(exec\.Command|os\.(WriteFile|Rename|OpenFile|Remove|CreateTemp)|filepath\.(Join|Clean|Rel))\b/,
    check: {
      label: 'Filesystem & process boundary',
      question: 'Is the path confined inside the root, "--"-delimited in argv, and written atomically if durable?',
      conceptId: 'path-traversal-guard',
    },
  },
  {
    test: /\b(BeginTx|ExecContext|QueryRow|r\.Body|json\.NewDecoder|io\.ReadAll|MaxBytesReader)\b/,
    check: {
      label: 'I/O bounds & lock scope',
      question: 'Is input bounded before allocation, and is slow network/process I/O kept outside transactions and locks?',
      conceptId: 'boundary-parse-dont-validate',
    },
  },
  {
    test: /\bstrings\.(Contains|HasPrefix|HasSuffix|TrimPrefix|Split)\b/,
    check: {
      label: 'Canonical edge cases',
      question: 'Check empty strings, trailing slashes, backslashes, or sibling-prefix collisions ("/app" vs "/app-2").',
      conceptId: 'path-traversal-guard',
    },
  },
  {
    test: /\b(fmt\.Errorf|errors\.(Is|As|New))\b/,
    check: {
      label: 'Error chain contract',
      question: 'Should callers match this cause (%w) or should internal implementation details stay sealed (%v)?',
      conceptId: 'error-wrapping',
    },
  },
];

/** Extracts up to 2 high-signal senior review questions from changed lines in a hunk. */
export function seniorHunkChecks(path: string, hunkLines: DiffLine[]): SeniorHunkCheck[] {
  const changed = hunkLines
    .filter(line => line.kind === 'added' || line.kind === 'removed')
    .map(line => line.text)
    .join('\n');
  if (!changed.trim()) return [];

  if (isTestPath(path)) {
    if (/\bt\.Run\b/.test(changed) || /\bTest[A-Z]/.test(changed)) {
      return [{
        label: 'Test determinism & isolation',
        question: 'Does this test pin both the failure case and the boundary without sleeps, shared state, or branching in assertions?',
        conceptId: 'table-driven-tests',
      }];
    }
    return [];
  }

  const matches: SeniorHunkCheck[] = [];
  for (const rule of HUNK_RULES) {
    if (rule.test.test(changed)) {
      matches.push(rule.check);
      if (matches.length >= 2) break;
    }
  }
  return matches;
}
