// Curated micro-cards for Go idioms and software-engineering concepts surfaced
// in hunk walkthroughs and the project's Learning log. Reading a card never
// calls a model; asking how the project uses it opens Ask the code pre-filled.

export type ConceptCategory = 'Go idiom' | 'Concurrency' | 'Safety' | 'Testing' | 'Design' | 'Project pattern';

export interface ConceptGuide {
  title: string;
  category: ConceptCategory;
  what: string;
  inGo: string;
  code?: string;
  watchOut?: string;
  curated: boolean;
}

interface CatalogEntry extends Omit<ConceptGuide, 'curated'> {
  matches: string[];
}

const CATALOG: CatalogEntry[] = [
  {
    title: 'Error wrapping (%w)',
    category: 'Go idiom',
    matches: ['error wrapping', 'fmt.errorf', '%w', 'wrap error', 'wrapped error'],
    what: 'Adds caller context to a failure while keeping the underlying error inspectable up the call stack.',
    inGo: 'Return fmt.Errorf("action: %w", err) at boundary transitions; use %v instead when callers must not depend on the internal cause.',
    code: 'data, err := os.ReadFile(path)\nif err != nil {\n\treturn fmt.Errorf("read config %s: %w", path, err)\n}',
    watchOut: 'Avoid logging and also returning the wrapped error, which prints every failure twice.',
  },
  {
    title: 'errors.Is & errors.As',
    category: 'Go idiom',
    matches: ['errors.is', 'errors.as', 'sentinel error', 'typed error', 'unwrap'],
    what: 'Inspects a wrapped error chain for a sentinel value (errors.Is) or a concrete error type (errors.As).',
    inGo: 'Prefer errors.Is(err, os.ErrNotExist) over == so wrapped errors still match cleanly.',
    code: 'var pathErr *os.PathError\nif errors.As(err, &pathErr) {\n\treturn pathErr.Path\n}',
    watchOut: 'Direct err == target comparisons break as soon as an intermediate function wraps err with %w.',
  },
  {
    title: 'Context cancellation & deadlines',
    category: 'Concurrency',
    matches: ['context', 'cancellation', 'context.context', 'deadline', 'timeout', 'withcancel', 'withtimeout'],
    what: 'Propagates request lifetimes, timeouts, and abort signals across I/O, child processes, and goroutines.',
    inGo: 'Pass ctx context.Context as the first parameter and select on <-ctx.Done() in blocking loops.',
    code: 'ctx, cancel := context.WithTimeout(parent, 5*time.Second)\ndefer cancel()\nreturn exec.CommandContext(ctx, "git", "status").Run()',
    watchOut: 'Always defer cancel() immediately after WithCancel or WithTimeout so timers and goroutines do not leak.',
  },
  {
    title: 'defer cleanup',
    category: 'Go idiom',
    matches: ['defer', 'defer cleanup', 'defer unlock', 'defer close', 'resource cleanup'],
    what: 'Schedules cleanup right next to acquisition so every return path releases locks, files, or timers.',
    inGo: 'Deferred calls run LIFO when the surrounding function returns, not when an inner block exits.',
    code: 'mu.Lock()\ndefer mu.Unlock()\nreturn updateLocked(state)',
    watchOut: 'Avoid defer inside a long loop — deferred resources stay open until the whole function returns.',
  },
  {
    title: 'Pointer vs value receiver',
    category: 'Go idiom',
    matches: ['pointer receiver', 'value receiver', 'method receiver', 'receiver'],
    what: 'Chooses whether a method mutates the caller’s struct in place (*T) or operates on an isolated copy (T).',
    inGo: 'Use a pointer receiver when the method mutates state, holds a sync.Mutex, or wraps a large struct; keep receivers consistent on a type.',
    code: 'func (s *Store) Put(k, v string) {\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\ts.items[k] = v\n}',
    watchOut: 'A value receiver copies the struct — copying a sync.Mutex or mutating a field silently discards changes.',
  },
  {
    title: 'Zero values',
    category: 'Go idiom',
    matches: ['zero value', 'zero-value', 'usable zero'],
    what: 'Designs structs so their default uninitialized state (var x T) works safely without a constructor.',
    inGo: 'sync.Mutex, bytes.Buffer, and slices work at their zero value; maps and channels need make before writes.',
    code: 'var buf bytes.Buffer // ready to Write without initialization\nbuf.WriteString("ok")',
    watchOut: 'Writing to a nil map panics at runtime even though reading from a nil map safely returns the zero value.',
  },
  {
    title: 'Nil slice vs empty slice',
    category: 'Go idiom',
    matches: ['nil slice', 'empty slice', 'slice capacity', 'slice append', 'slice aliasing'],
    what: 'Both len(nil) and len([]T{}) are 0, and append/range work identically on both, but JSON encodes nil as null.',
    inGo: 'Prefer var out []Item internally, and []Item{} at JSON API boundaries when clients expect [] instead of null.',
    code: 'items := make([]string, 0, len(raw))\nfor _, v := range raw {\n\titems = append(items, strings.TrimSpace(v))\n}',
    watchOut: 'Sub-slicing a[:n] shares the backing array; appending to it can overwrite elements of the original slice.',
  },
  {
    title: 'Mutex ownership & data races',
    category: 'Concurrency',
    matches: ['mutex', 'sync.mutex', 'rwmutex', 'data race', 'race condition', 'mutex lock'],
    what: 'Serializes reads and writes to shared memory so concurrent goroutines never observe torn or stale state.',
    inGo: 'Keep the mutex directly above the fields it guards and never perform slow network/disk I/O while holding a lock.',
    code: 's.mu.Lock()\nsnapshot := slices.Clone(s.entries)\ns.mu.Unlock()',
    watchOut: 'Returning a slice or map reference guarded by a mutex lets callers read or mutate it after Unlock.',
  },
  {
    title: 'Goroutine lifecycle',
    category: 'Concurrency',
    matches: ['goroutine', 'waitgroup', 'errgroup', 'goroutine leak', 'concurrency'],
    what: 'Every spawned goroutine needs a clear owner, a stop signal, and a join point before its parent returns.',
    inGo: 'Pair background goroutines with sync.WaitGroup or errgroup.WithContext so tests and shutdowns wait deterministically.',
    code: 'var wg sync.WaitGroup\nwg.Go(func() {\n\tworker(ctx)\n})\nwg.Wait()',
    watchOut: 'Sending on an unbuffered channel with no receiver blocks a goroutine forever if the reader exits early on error.',
  },
  {
    title: 'Channel ownership & closing',
    category: 'Concurrency',
    matches: ['channel', 'buffered channel', 'close channel', 'select'],
    what: 'Coordinates work or signals completion between goroutines without shared mutable state.',
    inGo: 'Only the sender closes a channel to mean “no more values”; use close(done) to broadcast cancellation to many readers.',
    code: 'select {\ncase out <- result:\ncase <-ctx.Done():\n\treturn ctx.Err()\n}',
    watchOut: 'Sending to a closed channel panics; closing from the receiver side races with in-flight senders.',
  },
  {
    title: 'Small consumer-side interfaces',
    category: 'Design',
    matches: ['interface', 'small interface', 'dependency inversion', 'decoupling', 'accept interfaces'],
    what: 'Defines narrow interfaces where a dependency is consumed rather than exporting giant interfaces from the provider.',
    inGo: 'Go interfaces are satisfied implicitly — a 1–2 method interface makes testing with a plain struct trivial.',
    code: 'type TokenReader interface {\n\tReadToken(ctx context.Context) (string, error)\n}',
    watchOut: 'Watch for typed-nil traps: an interface holding (*MyErr)(nil) is non-nil (err != nil is true!).',
  },
  {
    title: 'Table-driven tests',
    category: 'Testing',
    matches: ['table-driven', 'table driven', 'subtest', 't.run', 't.helper', 't.cleanup'],
    what: 'Runs one clear assertion body across named input/expectation cases, isolating edge cases with t.Run.',
    inGo: 'Pair t.Run(tc.name, ...) with t.Helper() on test helpers and t.TempDir() / t.Cleanup() for deterministic teardown.',
    code: 'for _, tc := range cases {\n\tt.Run(tc.name, func(t *testing.T) {\n\t\tif got := Parse(tc.in); got != tc.want {\n\t\t\tt.Fatalf("Parse(%q) = %v, want %v", tc.in, got, tc.want)\n\t\t}\n\t})\n}',
    watchOut: 'Keep table bodies assertion-only — complex branching inside the loop hides which case actually failed.',
  },
  {
    title: 'Early return (guard clauses)',
    category: 'Go idiom',
    matches: ['early return', 'guard clause', 'happy path', 'line of sight'],
    what: 'Handles invalid inputs, edge cases, and errors up front so the normal flow stays unindented along the left margin.',
    inGo: 'Prefer if err != nil { return err } over nested else blocks to keep cyclomatic complexity low.',
    code: 'if strings.TrimSpace(name) == "" {\n\treturn ErrEmptyName\n}\nreturn save(name)',
    watchOut: 'When refactoring to early returns, ensure resources opened earlier in the function have a defer cleanup.',
  },
  {
    title: 'Path traversal guard',
    category: 'Safety',
    matches: ['path traversal', 'filepath.clean', 'filepath.rel', 'directory traversal', 'symlink', 'path validation'],
    what: 'Prevents untrusted relative paths (like ../../etc/passwd or symlinks) from escaping the intended workspace root.',
    inGo: 'Clean and join under root, resolve symlinks when touching disk, and verify filepath.Rel stays inside root.',
    code: 'rel, err := filepath.Rel(root, target)\nif err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {\n\treturn ErrOutsideRoot\n}',
    watchOut: 'filepath.Join("/app", "../secret") resolves to "/secret" outside /app — always check filepath.Rel after joining.',
  },
  {
    title: 'Atomic file write',
    category: 'Safety',
    matches: ['atomic write', 'temp file', 'rename', 'crash safety', 'fsync', 'durable'],
    what: 'Writes state to a sibling temporary file, syncs it, and renames it into place so crashes never leave half-written data.',
    inGo: 'Create the temp file in the same directory as the destination so os.Rename stays on the same filesystem.',
    code: 'tmp := path + ".tmp"\nif err := os.WriteFile(tmp, data, 0o600); err != nil {\n\treturn err\n}\nreturn os.Rename(tmp, path)',
    watchOut: 'os.Rename across /tmp and another mount fails with EXDEV; always stage beside the target file.',
  },
  {
    title: 'Constant-time & salted secrets',
    category: 'Safety',
    matches: ['constant-time', 'constant time', 'subtle.constanttimecompare', 'salt', 'argon2', 'bcrypt', 'pbkdf2', 'kdf', 'hmac', 'crypto/rand'],
    what: 'Protects credentials and tokens against timing side-channels and rainbow-table precomputation.',
    inGo: 'Generate per-secret random salts with crypto/rand (never math/rand) and compare MACs/tokens with subtle.ConstantTimeCompare.',
    code: 'if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {\n\treturn ErrInvalidToken\n}',
    watchOut: 'subtle.ConstantTimeCompare returns 0 immediately when lengths differ — hash or check fixed-size digests first.',
  },
  {
    title: 'Boundary validation & parse, don’t validate',
    category: 'Safety',
    matches: ['boundary validation', 'input validation', 'allowlist', 'json.decoder', 'disallowunknownfields', 'sanitization'],
    what: 'Rejects malformed, oversized, or ambiguous external input at the HTTP/CLI edge before domain code runs.',
    inGo: 'Bound request bodies with http.MaxBytesReader / io.LimitReader and decode into typed domain structs.',
    code: 'dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))\ndec.DisallowUnknownFields()\nreturn dec.Decode(&req)',
    watchOut: 'Avoid scattering partial string checks deep in domain helpers where callers assume invariants already hold.',
  },
  {
    title: 'Idempotency & guarded optimistic concurrency',
    category: 'Design',
    matches: ['idempotency', 'idempotent', 'optimistic concurrency', 'revision', 'content hash', 'cas', 'compare-and-swap'],
    what: 'Ensures retries produce the same result and mutations fail cleanly (409 Conflict) if underlying state changed since review.',
    inGo: 'Bind write requests to the exact revision or SHA-256 content hash the user inspected before mutating state.',
    code: 'if current.Hash != req.ExpectedHash {\n\treturn ErrRevisionConflict\n}',
    watchOut: 'Check-then-write without holding the state lock lets two concurrent requests pass the same hash check.',
  },
];

function normalize(text: string): string {
  return text.trim().toLowerCase().replace(/[_-]+/g, ' ').replace(/\s+/g, ' ');
}

function containsPhrase(haystack: string, phrase: string): boolean {
  const at = haystack.indexOf(phrase);
  if (at < 0) return false;
  const before = at === 0 || haystack[at - 1] === ' ' || haystack[at - 1] === '/';
  const end = at + phrase.length;
  const after = end === haystack.length || haystack[end] === ' ' || haystack[end] === '/' || haystack[end] === 's';
  return before && after;
}

/** Resolves a concept tag to a curated Go/engineering micro-card, or a clean project-pattern fallback. */
export function lookupConcept(raw: string): ConceptGuide {
  const key = normalize(raw);
  if (!key) {
    return {
      title: raw || 'Concept',
      category: 'Project pattern',
      what: 'A pattern surfaced in this change.',
      inGo: 'Inspect the hunk beside this tag or ask how this codebase applies it across files.',
      curated: false,
    };
  }
  let best: { entry: CatalogEntry; score: number } | null = null;
  for (const entry of CATALOG) {
    for (const candidate of entry.matches) {
      const target = normalize(candidate);
      let score = 0;
      if (key === target) score = 100 + target.length;
      else if (containsPhrase(key, target)) score = 60 + target.length;
      else if (key.length >= 4 && containsPhrase(target, key)) score = 40 + key.length;
      if (score > (best?.score ?? 0)) best = { entry, score };
    }
  }
  if (best) {
    const { matches: _, ...guide } = best.entry;
    return { ...guide, curated: true };
  }
  return {
    title: raw.trim(),
    category: 'Project pattern',
    what: `“${raw.trim()}” is an engineering pattern or domain idiom highlighted in this change.`,
    inGo: 'Accepted tasks record every occurrence in your Learning log so you can trace how this project uses it.',
    curated: false,
  };
}

/** Question handed to Ask the code when exploring a concept in the active project. */
export function conceptQuestion(concept: string): string {
  return `Explain how this project uses ${concept}: where it appears, why it is used there, and what a newcomer should know about it.`;
}
