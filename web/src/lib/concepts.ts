// Curated Senior Engineering, Computer Science & System Design Atlas cards,
// surfaced in hunk walkthroughs, the Engineering Atlas, and the Learning log.
// Reading a card or answering its Socratic self-check never calls a model;
// asking how the project uses it opens Ask the code pre-filled.

import type { Lesson } from '@/api/types';

export type ConceptCategory = 'Go idiom' | 'Concurrency' | 'Safety' | 'Testing' | 'Design' | 'Project pattern';

export type AtlasDomain =
  | 'Concurrency & Memory'
  | 'Distributed Systems'
  | 'Storage & Durability'
  | 'Algorithms & Data Structures'
  | 'Architecture & Boundaries'
  | 'Security & Trust'
  | 'Language & Verification';

export interface AtlasDomainInfo {
  id: AtlasDomain;
  short: string;
  summary: string;
  focus: string;
}

export const ATLAS_DOMAINS: readonly AtlasDomainInfo[] = [
  {
    id: 'Concurrency & Memory',
    short: 'Concurrency',
    summary: 'Happens-before guarantees, structured lifecycles, lock contention, cancellation trees, and bounded queues.',
    focus: 'Who owns the goroutine, what guards the memory, and how does work stop?',
  },
  {
    id: 'Distributed Systems',
    short: 'Distributed',
    summary: 'Idempotency, optimistic CAS, tail latency, retry storms, circuit breakers, and lease fencing.',
    focus: 'What happens when a call is slow, retried, reordered, or partially applied?',
  },
  {
    id: 'Storage & Durability',
    short: 'Storage',
    summary: 'Crash-atomic file writes, WAL vs B-Tree trade-offs, snapshot isolation, content hashing, and cache coherence.',
    focus: 'What survives a crash mid-write, and what invariant binds cached or on-disk state?',
  },
  {
    id: 'Algorithms & Data Structures',
    short: 'Algorithms',
    summary: 'Amortized complexity, graph traversals & cycle detection, AST visitors, streaming I/O, and cache locality.',
    focus: 'How do time, allocations, and working-set memory scale with adversarial or 100x input?',
  },
  {
    id: 'Architecture & Boundaries',
    short: 'Architecture',
    summary: 'Acyclic dependencies, Martin instability (I), consumer-owned interfaces, leaf packages, and explicit state machines.',
    focus: 'Which direction do dependencies point, and can illegal states be represented?',
  },
  {
    id: 'Security & Trust',
    short: 'Security',
    summary: 'Path containment, parse-don’t-validate edges, constant-time secret verification, and capability/sandbox boundaries.',
    focus: 'Where is the trust boundary, and is untrusted input constrained before domain logic runs?',
  },
  {
    id: 'Language & Verification',
    short: 'Craft & Tests',
    summary: 'Error chains, deterministic resource cleanup, value/pointer semantics, zero-value invariants, and subtest isolation.',
    focus: 'Are failure modes inspectable, resources paired with cleanup, and edge cases pinned by deterministic tests?',
  },
];

export interface ConceptSelfCheck {
  prompt: string;
  answer: string;
}

export interface ConceptGuide {
  id: string;
  title: string;
  category: ConceptCategory;
  domain: AtlasDomain;
  what: string;
  mentalModel: string;
  inGo: string;
  complexity?: string;
  tradeOffs?: string;
  failureModes?: string;
  code?: string;
  watchOut?: string;
  selfCheck?: ConceptSelfCheck;
  curated: boolean;
}

interface CatalogSpec {
  id: string;
  title: string;
  category: ConceptCategory;
  domain: AtlasDomain;
  matches: string[];
  what: string;
  mentalModel: string;
  inGo: string;
  complexity?: string;
  tradeOffs?: string;
  failureModes?: string;
  code?: string;
  watchOut?: string;
  selfCheck?: ConceptSelfCheck;
}

const spec = (
  id: string, title: string, category: ConceptCategory, domain: AtlasDomain, matches: string[],
  what: string, mentalModel: string, inGo: string,
  extra: Pick<CatalogSpec, 'complexity' | 'tradeOffs' | 'failureModes' | 'code' | 'watchOut' | 'selfCheck'>,
): CatalogSpec => ({ id, title, category, domain, matches, what, mentalModel, inGo, ...extra });

const CATALOG: CatalogSpec[] = [
  // ── Language & Verification ─────────────────────────────────────────────
  spec('error-wrapping', 'Error wrapping (%w)', 'Go idiom', 'Language & Verification',
    ['error wrapping', 'fmt.errorf', '%w', 'wrap error', 'wrapped error'],
    'Adds caller context to a failure while keeping the underlying error inspectable up the call stack.',
    'Think of an error chain as a linked list of causal frames: each boundary prepends what it was trying to do without erasing why it failed.',
    'Return fmt.Errorf("action: %w", err) at boundary transitions; use %v instead when callers must not depend on internal implementation errors.',
    {
      complexity: 'O(depth) chain traversal on errors.Is/As; 1 small heap allocation per fmt.Errorf frame.',
      tradeOffs: '%w preserves programmatic inspection (errors.Is/As) but couples callers to the wrapped error as part of your contract; %v seals the abstraction boundary.',
      failureModes: 'Wrapping internal driver errors across public APIs leaks implementation details; logging and returning logs every failure twice.',
      code: 'data, err := os.ReadFile(path)\nif err != nil {\n\treturn fmt.Errorf("read config %s: %w", path, err)\n}',
      watchOut: 'Avoid logging and also returning the wrapped error, which prints every failure twice.',
      selfCheck: { prompt: 'When should a package boundary use %v instead of %w?', answer: 'When the underlying error comes from an internal dependency (e.g. SQLite or HTTP transport) that callers should not match on or couple to.' },
    }),
  spec('errors-is-as', 'errors.Is & errors.As', 'Go idiom', 'Language & Verification',
    ['errors.is', 'errors.as', 'sentinel error', 'typed error', 'unwrap'],
    'Inspects a wrapped error chain for a sentinel value (errors.Is) or a concrete error type (errors.As).',
    'Walks the Unwrap() tree (including multi-errors from errors.Join) so error classification survives layers of context.',
    'Prefer errors.Is(err, os.ErrNotExist) over == and pass a pointer to the target interface/pointer in errors.As(err, &target).',
    {
      complexity: 'O(nodes) DFS over Unwrap() / []error trees, typically 1–5 frames.',
      tradeOffs: 'Sentinel values are simple for fixed states (ErrNotFound); typed structs carry structured fields (path, status, retry-after) at the cost of a type definition.',
      failureModes: 'Direct err == target checks silently fail as soon as an intermediate helper wraps err with fmt.Errorf("%w").',
      code: 'var pathErr *os.PathError\nif errors.As(err, &pathErr) {\n\treturn pathErr.Path\n}',
      watchOut: 'Direct err == target comparisons break as soon as an intermediate function wraps err with %w.',
      selfCheck: { prompt: 'Why must the second argument to errors.As be a pointer to an error type (e.g. &pathErr)?', answer: 'Because errors.As writes the matched concrete error value into your variable so you can read its structured fields.' },
    }),
  spec('defer-cleanup', 'defer cleanup', 'Go idiom', 'Language & Verification',
    ['defer', 'defer cleanup', 'defer unlock', 'defer close', 'resource cleanup'],
    'Schedules cleanup right next to acquisition so every return path—including panics—releases locks, files, or timers.',
    'Lexical RAII at function scope: pairing acquire + defer release in adjacent lines eliminates forgotten-unlock bugs across early returns.',
    'Deferred calls evaluate their arguments immediately when scheduled and execute LIFO when the surrounding function returns.',
    {
      complexity: 'Near-zero open-coded stack overhead (~ns) in modern Go unless scheduled inside a dynamic loop.',
      tradeOffs: 'Guarantees release on every branch, but ties lifetime to the enclosing function rather than an inner lexical block.',
      failureModes: 'Deferring file.Close() on a writable file without checking its error can drop buffered flush failures; defer in a loop pins FDs until return.',
      code: 'mu.Lock()\ndefer mu.Unlock()\nreturn updateLocked(state)',
      watchOut: 'Avoid defer inside a long loop — deferred resources stay open until the whole function returns.',
      selfCheck: { prompt: 'When are the arguments to a deferred function call evaluated?', answer: 'Immediately when the defer statement executes—not when the surrounding function returns (unless wrapped in a closure).' },
    }),
  spec('pointer-value-receiver', 'Pointer vs value receiver', 'Go idiom', 'Language & Verification',
    ['pointer receiver', 'value receiver', 'method receiver', 'receiver'],
    'Chooses whether a method mutates the caller’s struct in place (*T) or operates on an isolated stack-friendly copy (T).',
    'Value receivers model immutable value types (time.Time, netip.Addr); pointer receivers model stateful entities with identity or locks.',
    'Use *T when mutating state, holding sync primitives, or avoiding large struct copies; keep all methods on T consistent.',
    {
      complexity: 'Value copy costs O(sizeof(T)) words on call; pointers copy 1 word but may cause heap escape and pointer chasing.',
      tradeOffs: 'Immutable values eliminate aliasing bugs and GC pressure; pointers share mutable state and satisfy interfaces expecting *T.',
      failureModes: 'Copying a struct containing sync.Mutex or atomic fields by value splits lock state and triggers go vet copylocks bugs.',
      code: 'func (s *Store) Put(k, v string) {\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\ts.items[k] = v\n}',
      watchOut: 'A value receiver copies the struct — copying a sync.Mutex or mutating a field silently discards changes.',
      selfCheck: { prompt: 'Does a value of type T satisfy an interface if one required method has a pointer receiver (*T)?', answer: 'No. The method set of T includes only value-receiver methods, whereas *T includes both *T and T methods.' },
    }),
  spec('zero-values', 'Zero values', 'Go idiom', 'Language & Verification',
    ['zero value', 'zero-value', 'usable zero'],
    'Designs structs so their default zero-initialized memory (var x T) satisfies invariants without mandatory constructors.',
    'Every Go allocation starts zeroed; aligning zero bits with the valid initial state removes "uninitialized object" states.',
    'sync.Mutex, bytes.Buffer, and slices work at their zero value; lazily initialize internal maps on first write when practical.',
    {
      complexity: 'O(1) bulk zeroing by the allocator; avoids extra heap indirection from constructor-only wrappers.',
      tradeOffs: 'Simplifies embedding and composition, though types requiring mandatory external handles (DB, credentials) still need explicit constructors.',
      failureModes: 'Writing to a nil map or closing a nil channel panics at runtime if lazy initialization is omitted.',
      code: 'var buf bytes.Buffer // ready to Write without initialization\nbuf.WriteString("ok")',
      watchOut: 'Writing to a nil map panics at runtime even though reading from a nil map safely returns the zero value.',
      selfCheck: { prompt: 'Why design boolean config fields so false is the safe default (e.g. AllowInsecure instead of Secure)?', answer: 'So an omitted field in T{} defaults to the safe behavior without requiring callers to set every flag.' },
    }),
  spec('table-driven-tests', 'Table-driven tests', 'Testing', 'Language & Verification',
    ['table-driven', 'table driven', 'subtest', 't.run', 't.helper', 't.cleanup'],
    'Runs one clear assertion body across named input/expectation cases, isolating boundary conditions with t.Run.',
    'Separates the specification space (inputs → expected invariants) from the execution harness so adding a regression case is 1 line.',
    'Pair t.Run(tc.name, ...) with t.Helper() on assertion helpers and t.TempDir() / t.Cleanup() for deterministic teardown.',
    {
      complexity: 'Linear in cases; subtests can run go test -run TestX/case_name in O(1) targeted iteration.',
      tradeOffs: 'Great for pure functions and protocol edge cases; becomes unreadable if a single table mixes unrelated setup topologies.',
      failureModes: 'Putting if/else logic inside the assertion loop to compute expected values duplicates production bugs in the test.',
      code: 'for _, tc := range cases {\n\tt.Run(tc.name, func(t *testing.T) {\n\t\tif got := Parse(tc.in); got != tc.want {\n\t\t\tt.Fatalf("Parse(%q) = %v, want %v", tc.in, got, tc.want)\n\t\t}\n\t})\n}',
      watchOut: 'Keep table bodies assertion-only — complex branching inside the loop hides which case actually failed.',
      selfCheck: { prompt: 'When does a table-driven test hurt clarity more than separate test functions?', answer: 'When cases require different stateful fixtures, mocks, or multi-step lifecycles, forcing conditional branching inside the loop.' },
    }),
  spec('early-return', 'Early return (guard clauses)', 'Go idiom', 'Language & Verification',
    ['early return', 'guard clause', 'happy path', 'line of sight'],
    'Rejects invalid inputs, edge cases, and errors up front so the normal flow stays unindented along the left margin.',
    'Each guard clause narrows the proof obligation for the rest of the function—after line N, preconditions are guaranteed.',
    'Prefer if err != nil { return err } and precondition guards over nested else trees to keep cyclomatic complexity low.',
    {
      complexity: 'Same runtime branches, O(1) human working-memory depth instead of O(nesting) state tracking.',
      tradeOffs: 'Flattens control flow dramatically; requires disciplined defer cleanup so early exits never leak acquired resources.',
      failureModes: 'Adding an early return mid-function after manual Lock() (without defer Unlock()) deadlocks subsequent callers.',
      code: 'if strings.TrimSpace(name) == "" {\n\treturn ErrEmptyName\n}\nreturn save(name)',
      watchOut: 'When refactoring to early returns, ensure resources opened earlier in the function have a defer cleanup.',
      selfCheck: { prompt: 'How do guard clauses affect cyclomatic complexity vs cognitive complexity?', answer: 'Branch count stays similar, but cognitive complexity drops sharply because readers do not hold nested negations in working memory.' },
    }),

  // ── Concurrency & Memory ────────────────────────────────────────────────
  spec('context-cancellation', 'Context cancellation & deadlines', 'Concurrency', 'Concurrency & Memory',
    ['context', 'cancellation', 'context.context', 'deadline', 'timeout', 'withcancel', 'withtimeout'],
    'Propagates request lifetimes, timeouts, and abort signals down a call tree across I/O, child processes, and goroutines.',
    'A hierarchical cancellation tree: cancelling a parent node atomically closes Done() on every descendant.',
    'Pass ctx context.Context as the first parameter; never store it on a long-lived struct; select on <-ctx.Done() in blocking loops.',
    {
      complexity: 'O(children) broadcast on cancel; 1 timer/goroutine attachment per WithTimeout until cancel() runs.',
      tradeOffs: 'Cooperative cancellation keeps invariants intact (unlike thread kill) but requires every blocking call in the chain to honor ctx.',
      failureModes: 'Forgetting defer cancel() leaks timer attachments until the parent context dies; passing request ctx to detached background work aborts it prematurely.',
      code: 'ctx, cancel := context.WithTimeout(parent, 5*time.Second)\ndefer cancel()\nreturn exec.CommandContext(ctx, "git", "status").Run()',
      watchOut: 'Always defer cancel() immediately after WithCancel or WithTimeout so timers and goroutines do not leak.',
      selfCheck: { prompt: 'What happens if an HTTP handler spawns a background audit write using r.Context()?', answer: 'As soon as the HTTP response is written, net/http cancels r.Context(), aborting the in-flight audit write.' },
    }),
  spec('mutex-ownership', 'Mutex ownership & data races', 'Concurrency', 'Concurrency & Memory',
    ['mutex', 'sync.mutex', 'rwmutex', 'data race', 'race condition', 'mutex lock'],
    'Establishes a happens-before memory barrier around shared state so concurrent goroutines never observe torn writes or stale caches.',
    'Unlock(m) happens-before the next Lock(m): a mutex protects data invariants, not code lines.',
    'Declare mu sync.Mutex directly above the fields it guards, clone slices/maps before returning them, and never do I/O under a lock.',
    {
      complexity: 'Uncontended Lock/Unlock is ~15ns atomic CAS; contended locks park goroutines and serialize throughput (Amdahl’s law).',
      tradeOffs: 'sync.Mutex is simpler and often faster than channels for in-memory state caches; RWMutex helps read-heavy paths but adds writer-starvation bookkeeping.',
      failureModes: 'Holding two locks in inconsistent order causes AB-BA deadlock; returning an internal map/slice reference leaks a data race after Unlock.',
      code: 's.mu.Lock()\nsnapshot := slices.Clone(s.entries)\ns.mu.Unlock()',
      watchOut: 'Returning a slice or map reference guarded by a mutex lets callers read or mutate it after Unlock.',
      selfCheck: { prompt: 'Why is reading a shared bool or int64 without a lock or sync/atomic undefined in Go?', answer: 'Without a synchronization edge, the compiler/CPU can reorder loads, cache registers indefinitely, or tear multi-word values.' },
    }),
  spec('goroutine-lifecycle', 'Goroutine lifecycle', 'Concurrency', 'Concurrency & Memory',
    ['goroutine', 'waitgroup', 'errgroup', 'goroutine leak', 'concurrency', 'structured concurrency'],
    'Binds every spawned goroutine to a clear lexical owner, a cancellation signal, and a deterministic join point.',
    'Structured concurrency: a parent scope must not return while child goroutines it spawned are still touching memory or FDs.',
    'Pair workers with sync.WaitGroup or errgroup.WithContext(ctx) so shutdowns and tests wait cleanly with zero leaks.',
    {
      complexity: '2 KiB initial growable stack per goroutine; leaked goroutines permanently pin their stack and reachable heap graph.',
      tradeOffs: 'Cheap concurrency enables clear request-per-task code, but unbounded go fn() under load exhausts memory and downstream pools.',
      failureModes: 'Unbuffered error channel errc <- err blocks forever if the parent returns on the first error without draining remaining workers.',
      code: 'var wg sync.WaitGroup\nwg.Go(func() {\n\tworker(ctx)\n})\nwg.Wait()',
      watchOut: 'Sending on an unbuffered channel with no receiver blocks a goroutine forever if the reader exits early on error.',
      selfCheck: { prompt: 'In a fan-out of N workers reporting to errc, why make errc buffered of size N (or use errgroup)?', answer: 'If worker 1 fails and the parent returns early, workers 2..N can still send their result without blocking forever on errc.' },
    }),
  spec('channel-ownership', 'Channel ownership & closing', 'Concurrency', 'Concurrency & Memory',
    ['channel', 'buffered channel', 'close channel', 'select'],
    'Transfers ownership of values or broadcasts completion signals across goroutines with built-in happens-before synchronization.',
    'Assign strict roles: 1 owner creates, writes, and closes the channel; consumers only read until closed or ctx.Done().',
    'Only the sender closes data channels; use close(done) on an empty-struct channel to broadcast a one-shot signal to N readers.',
    {
      complexity: 'Channel send/recv acquires an internal runtime lock + copies payload (~50ns uncontended).',
      tradeOffs: 'Great for pipelines, cancellation broadcasts, and bounded semaphores; awkward for random-access state lookup (use a mutex).',
      failureModes: 'Sending to a closed channel panics; reading from a nil channel blocks forever (useful to disable a select arm, fatal by accident).',
      code: 'select {\ncase out <- result:\ncase <-ctx.Done():\n\treturn ctx.Err()\n}',
      watchOut: 'Sending to a closed channel panics; closing from the receiver side races with in-flight senders.',
      selfCheck: { prompt: 'How do you safely stop M concurrent senders writing into one shared channel?', answer: 'Never close the data channel from a receiver; cancel a shared ctx (or close a separate stop channel) and close the data channel after wg.Wait() joins all M senders.' },
    }),
  spec('slice-aliasing', 'Nil slice vs empty slice', 'Go idiom', 'Concurrency & Memory',
    ['nil slice', 'empty slice', 'slice capacity', 'slice append', 'slice aliasing'],
    'Models dynamic views (ptr, len, cap) over a contiguous backing array—fast and cache-friendly, but shared until reallocation.',
    'A slice header is a 3-word window into an array: two slices sharing spare capacity alias the exact same memory.',
    'Prefer var out []Item internally, []Item{} at JSON boundaries when [] is required, and full-slice a[:n:n] when preventing append aliasing.',
    {
      complexity: 'O(1) slicing; O(1) amortized append via geometric ~1.25x–2x capacity growth; O(N) pin if a tiny slice references a huge buffer.',
      tradeOffs: 'Zero-copy slicing avoids allocations in parsers, but keeps the entire underlying buffer live in GC until cloned.',
      failureModes: 'Appending to prefix := shared[:2] mutates shared[2] in place if cap(shared) > 2, corrupting concurrent readers.',
      code: 'items := make([]string, 0, len(raw))\nfor _, v := range raw {\n\titems = append(items, strings.TrimSpace(v))\n}',
      watchOut: 'Sub-slicing a[:n] shares the backing array; appending to it can overwrite elements of the original slice.',
      selfCheck: { prompt: 'How does the 3-index slice expression buf[:n:n] prevent silent mutation bugs?', answer: 'It caps capacity to n, forcing any future append() on the sub-slice to allocate a fresh backing array instead of overwriting buf[n].' },
    }),
  spec('backpressure-bounded-queues', 'Backpressure & bounded concurrency', 'Concurrency', 'Concurrency & Memory',
    ['backpressure', 'bounded queue', 'semaphore', 'worker pool', 'rate limit', 'load shedding'],
    'Caps in-flight work and queue depth so traffic spikes degrade predictably instead of exhausting RAM, FDs, or CPU.',
    'Little’s Law (L = λW): unbounded queues turn temporary slowness (high W) into unbounded memory growth (L) and timeout cascades.',
    'Bound concurrency with errgroup.SetLimit(n) or a weighted semaphore, and bound queues with fixed-size channels + deadline rejection.',
    {
      complexity: 'O(limit) worst-case resident work; O(1) admission check per request.',
      tradeOffs: 'Small queues fail fast and keep tail latency low; large buffers absorb micro-bursts at the cost of memory and queueing delay.',
      failureModes: 'Accepting work faster than downstream drains while callers time out wastes 100% of CPU serving already-abandoned requests.',
      code: 'var g errgroup.Group\ng.SetLimit(4)\nfor _, job := range jobs {\n\tg.Go(func() error { return run(ctx, job) })\n}\nreturn g.Wait()',
      watchOut: 'Never size a queue to 100,000 "just in case"—queueing delay grows linearly with depth while callers have already timed out.',
      selfCheck: { prompt: 'By Little’s Law (L = λW), if arrival rate λ = 200 req/s and latency W spikes from 50ms to 2s, how many in-flight requests L accumulate?', answer: 'L jumps from 10 to 400 concurrent requests—40x memory and connection pressure from latency alone.' },
    }),

  // ── Distributed Systems ─────────────────────────────────────────────────
  spec('idempotency-cas', 'Idempotency & guarded optimistic concurrency', 'Design', 'Distributed Systems',
    ['idempotency', 'idempotent', 'optimistic concurrency', 'revision', 'content hash', 'cas', 'compare-and-swap'],
    'Ensures retried mutations apply at most once and state transitions fail cleanly (409 Conflict) if state drifted since review.',
    'Compare-And-Swap (CAS) at the domain level: bind every mutation to the exact revision/SHA-256 digest the actor approved.',
    'Verify expected revision/hash inside the same critical section or transaction that commits the write; record idempotency keys for side effects.',
    {
      complexity: 'O(1) version/digest check; zero lock hold time during slow human review or LLM generation.',
      tradeOffs: 'Optimistic concurrency scales effortlessly under low contention (no long locks), while high contention favors serialized queues.',
      failureModes: 'Checking current.Hash == req.Hash before acquiring the write lock creates a TOCTOU race where two writers overwrite each other.',
      code: 'if current.Hash != req.ExpectedHash {\n\treturn ErrRevisionConflict\n}',
      watchOut: 'Check-then-write without holding the state lock lets two concurrent requests pass the same hash check.',
      selfCheck: { prompt: 'Why is HTTP PUT /items/42 not automatically safe against lost updates without an If-Match / revision check?', answer: 'PUT is repeatable for the same payload, but two concurrent readers editing item 42 will silently clobber each other’s changes (last-write-wins).' },
    }),
  spec('timeouts-retries-jitter', 'Timeouts, exponential backoff & jitter', 'Concurrency', 'Distributed Systems',
    ['retry', 'exponential backoff', 'jitter', 'retry storm', 'thundering herd'],
    'Bounds every remote call with a deadline and spaces transient retries with randomized backoff so recovering services are not crushed.',
    'Without jitter, clients that failed together retry in synchronized waves—turning a 1-second blip into a self-sustaining outage.',
    'Retry only transient/idempotent failures, cap total attempts + parent ctx deadline, and sleep rand(0, min(cap, base * 2^attempt)).',
    {
      complexity: 'Bounded O(maxAttempts) work; full jitter spreads synchronized load uniformly across [0, window].',
      tradeOffs: 'Recovers seamlessly from transient packet/rate hiccups, but retrying at multiple layers (3×3×3 = 27x) amplifies outages catastrophically.',
      failureModes: 'Sleeping with time.Sleep(d) instead of a ctx-aware timer ignores cancellation and stalls graceful shutdown.',
      code: 'wait := time.Duration(rand.Int64N(int64(min(maxWait, base<<attempt))))\ntimer := time.NewTimer(wait)\nselect {\ncase <-timer.C:\ncase <-ctx.Done():\n\ttimer.Stop()\n\treturn ctx.Err()\n}',
      watchOut: 'Retry only at one well-defined boundary—nested retries across client, service, and gateway multiply load exponentially.',
      selfCheck: { prompt: 'Why use "full jitter" rand(0, base*2^n) instead of fixed exponential delays 1s, 2s, 4s?', answer: 'Fixed delays keep all callers synchronized in lockstep waves; randomization decorrelates retry bursts across time.' },
    }),
  spec('tail-latency-hedging', 'Tail latency (p99) & fan-out amplification', 'Design', 'Distributed Systems',
    ['tail latency', 'p99', 'fan-out', 'head-of-line blocking', 'latency amplification'],
    'Controls worst-case 99th-percentile slowdowns caused by GC pauses, lock contention, noisy neighbors, and parallel fan-out.',
    'If a parent waits on 20 parallel sub-calls each with a 1% chance of slowness, 1 - 0.99^20 ≈ 18.2% of parent requests hit the slow tail!',
    'Measure p50/p95/p99 histograms (never averages), batch N+1 calls, bound per-hop budgets, and degrade optional enrichments gracefully.',
    {
      complexity: 'Fan-out tail probability: P(any slow) = 1 - (1 - p)^k, approaching 1 rapidly as fan-out k grows.',
      tradeOffs: 'Parallelism cuts median wall-clock latency, but couples tail latency to the slowest dependency unless optional results have soft deadlines.',
      failureModes: 'Optimizing mean latency while ignoring p99 hides head-of-line blocking that stalls shared worker pools.',
      code: 'subCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)\ndefer cancel()\noptionalHints, _ := fetchOptionalHints(subCtx, id) // soft budget',
      watchOut: 'Averages lie: a service with 5ms average latency can still have multi-second p99 stalls for your busiest users.',
      selfCheck: { prompt: 'If a page fans out to 50 independent shards each having p99 = 500ms, roughly what fraction of page loads take ≥ 500ms?', answer: '1 - 0.99^50 ≈ 39.5%—nearly two in five requests experience the p99 tail.' },
    }),
  spec('circuit-breaker-bulkhead', 'Circuit breakers & bulkheads', 'Design', 'Distributed Systems',
    ['circuit breaker', 'bulkhead', 'fault isolation', 'graceful degradation', 'cascading failure'],
    'Isolates failing dependencies in separate resource pools (bulkheads) and short-circuits dead upstreams before threads saturate.',
    'Like watertight ship compartments: a leak in optional analytics or one LLM provider must never sink core local workspace reads.',
    'Give each external provider its own concurrency limiter and timeout; trip open after consecutive failures and probe recovery half-open.',
    {
      complexity: 'O(1) rolling counter / state check (Closed → Open → Half-Open) per outbound call.',
      tradeOffs: 'Fast failure preserves healthy subsystems and gives failing upstreams breathing room, at the cost of tuning false-trip thresholds.',
      failureModes: 'Sharing one global HTTP client/worker pool across critical and slow endpoints lets one stalled host starve everything.',
      code: 'if !breaker.Allow(now) {\n\treturn ErrProviderTemporarilyUnavailable\n}\nerr := callUpstream(ctx)\nbreaker.Record(now, err)',
      watchOut: 'Do not trip a circuit breaker on client 4xx validation errors—only count timeouts, connection failures, and 5xx/429 overload.',
      selfCheck: { prompt: 'What is the difference between a circuit breaker and a bulkhead?', answer: 'A bulkhead partitions capacity (separate pools per dependency) so one cannot starve others; a circuit breaker stops calling an unhealthy dependency altogether.' },
    }),
  spec('consensus-leases-fencing', 'Leases, fencing tokens & single-writer safety', 'Design', 'Distributed Systems',
    ['lease', 'fencing token', 'lock file', 'split brain', 'epoch', 'generation'],
    'Prevents two paused or overlapping processes from mutating the same resource concurrently via monotonic generation numbers.',
    'Time-based leases alone cannot guarantee mutual exclusion because a GC pause or OS suspend can outlast the lease TTL.',
    'Pair every lease or active run with a monotonically increasing generation/epoch token checked atomically by the storage layer.',
    {
      complexity: 'O(1) integer epoch comparison on every state transition.',
      tradeOffs: 'Requires storage to reject stale epochs (epoch < currentEpoch), eliminating split-brain corruption without distributed consensus overhead.',
      failureModes: 'Relying solely on "my lock TTL hasn’t expired according to my wall clock" fails under clock skew or process pauses.',
      code: 'if req.Generation != state.ActiveGeneration {\n\treturn ErrStaleRunGeneration\n}',
      watchOut: 'Never use time.Now() for monotonic elapsed checks when comparing lease durations; Go’s time.Since uses the monotonic clock.',
      selfCheck: { prompt: 'Why can a holder of a 10-second lock still corrupt storage if it checks its lease locally before writing?', answer: 'The process can pause (GC/scheduler) right after the check for 12 seconds while a new holder acquires the lock, then wake up and overwrite newer data unless storage checks a fencing token.' },
    }),

  // ── Storage & Durability ────────────────────────────────────────────────
  spec('atomic-file-write', 'Atomic file write', 'Safety', 'Storage & Durability',
    ['atomic write', 'temp file', 'rename', 'crash safety', 'fsync', 'durable'],
    'Writes state to a sibling temporary file, flushes it to media, and renames it atomically over the target path.',
    'POSIX rename within one directory is atomic: readers observe either the complete old file or the complete new file, never torn JSON.',
    'Create temp files in filepath.Dir(target) with os.CreateTemp, Sync() before Close(), and os.Rename(tmp, target).',
    {
      complexity: 'O(bytes) sequential write + 1 fsync media flush (~0.5–5ms on NVMe) + O(1) directory inode swap.',
      tradeOffs: 'Temporarily uses 2x file disk space during the write, in exchange for complete immunity to half-written corrupt files on crash.',
      failureModes: 'Staging in /tmp and renaming into /home fails with EXDEV across mount points; skipping Sync() can leave a 0-byte file after power loss.',
      code: 'tmp := path + ".tmp"\nif err := os.WriteFile(tmp, data, 0o600); err != nil {\n\treturn err\n}\nreturn os.Rename(tmp, path)',
      watchOut: 'os.Rename across /tmp and another mount fails with EXDEV; always stage beside the target file.',
      selfCheck: { prompt: 'Why does os.WriteFile(path, data, 0600) directly on an existing config file risk data loss on power failure?', answer: 'O_TRUNC truncates the existing file to 0 bytes first; a crash before write+flush completes leaves an empty or half-written file.' },
    }),
  spec('wal-lsm-btree', 'WAL, B-Trees & LSM storage trade-offs', 'Design', 'Storage & Durability',
    ['wal', 'write-ahead log', 'b-tree', 'lsm', 'append-only', 'compaction'],
    'Separates sequential durability logging (WAL) from indexed structures optimized for reads (B-Trees) or write throughput (LSM-Trees).',
    'Random disk mutation is slow and non-atomic; appending intent to a sequential log makes crash recovery deterministic replay.',
    'Use SQLite (B-Tree + WAL) for relational point/range queries and append-only journals when recording immutable audit/event streams.',
    {
      complexity: 'B-Tree: O(log_B N) page reads/writes; LSM: O(1) memtable append + amortized compaction write amplification.',
      tradeOffs: 'B-Trees excel at predictable low-latency reads and transactions; LSM-Trees excel at high ingest rates at the cost of compaction CPU/IO.',
      failureModes: 'Uncompacted append-only logs grow startup replay time to O(history) unless periodic snapshots/checkpoints truncate old segments.',
      code: '// Append-only event + periodic snapshot keeps replay O(delta)\nif err := log.Append(event); err != nil {\n\treturn err\n}',
      watchOut: 'An append-only log without checksums (CRC32/SHA-256) cannot distinguish a clean EOF from a torn trailing record after a crash.',
      selfCheck: { prompt: 'Why do B-Tree databases still write to a sequential Write-Ahead Log (WAL) first?', answer: 'So a multi-page B-Tree split is crash-atomic and durable with one sequential flush instead of random page fsyncs.' },
    }),
  spec('sqlite-wal-isolation', 'SQLite WAL & snapshot isolation', 'Design', 'Storage & Durability',
    ['sqlite', 'snapshot isolation', 'mvcc', 'busy_timeout', 'transaction', 'single writer'],
    'Allows concurrent readers to view a consistent point-in-time snapshot while a single serialized writer appends to the WAL.',
    'Readers never block writers and writers never block readers in WAL mode—only two concurrent writers contend.',
    'Enable PRAGMA journal_mode=WAL, PRAGMA busy_timeout, and PRAGMA foreign_keys=ON; keep write transactions short and pure.',
    {
      complexity: 'O(1) snapshot acquisition; writer throughput serialized to 1 OS thread at a time (~thousands of tx/s locally).',
      tradeOffs: 'Zero-ops single-file ACID durability, provided write transactions never hold locks across network or model calls.',
      failureModes: 'Starting BEGIN DEFERRED, reading, and upgrading to a write mid-transaction fails immediately with SQLITE_BUSY_SNAPSHOT; use BEGIN IMMEDIATE for read-modify-write.',
      code: 'tx, err := db.BeginTx(ctx, nil)\nif err != nil {\n\treturn err\n}\ndefer tx.Rollback()\n// ... mutations ...\nreturn tx.Commit()',
      watchOut: 'Never perform an LLM call or child process execution inside an open SQLite transaction—it blocks every other writer.',
      selfCheck: { prompt: 'Why can two goroutines sharing one Go sql.DB hit SQLITE_BUSY even with WAL enabled?', answer: 'sql.DB pools multiple connections; two connections upgrading to write concurrently contend unless serialized (MaxOpenConns(1) for writer) or configured with busy_timeout + BEGIN IMMEDIATE.' },
    }),
  spec('content-addressed-hashing', 'Content-addressed digests & Merkle identity', 'Design', 'Storage & Durability',
    ['content-addressed', 'sha256', 'digest', 'merkle', 'fingerprint', 'etag'],
    'Identifies files, diffs, and cached artifacts by the cryptographic hash of their canonical bytes rather than mutable timestamps.',
    'Same content ⇒ identical SHA-256 key: cache invalidation, staleness detection, and tamper checks become pure equality.',
    'Hash canonicalized inputs (sorted keys, length-prefixed fields) with crypto/sha256 so distinct tuples never collide.',
    {
      complexity: 'O(bytes) linear scan (~500 MB/s–2 GB/s with hardware SHA extensions); O(1) 32-byte equality check.',
      tradeOffs: 'Eliminates mtime clock-skew bugs and enables safe deduplication, at the cost of reading bytes once to compute the digest.',
      failureModes: 'Concatenatingsha256(a + b) without delimiters lets ("ab", "c") collide with ("a", "bc")—always length-prefix or NUL-delimit fields!',
      code: 'h := sha256.New()\nfmt.Fprintf(h, "%d:%s\\n%d:%s", len(path), path, len(body), body)\nsum := hex.EncodeToString(h.Sum(nil))',
      watchOut: 'Concatenating variable-length strings into a hash without length prefixes causes canonicalization collisions.',
      selfCheck: { prompt: 'Why is sha256(role + prompt) vulnerable to collision while sha256(len(role) + ":" + role + prompt) is not?', answer: 'Without length framing, ("admin", "user") and ("adminu", "ser") produce the exact same byte stream and identical SHA-256 hash.' },
    }),
  spec('cache-coherence-stampede', 'Cache invalidation & singleflight coalescing', 'Design', 'Storage & Durability',
    ['cache', 'invalidation', 'singleflight', 'cache stampede', 'memoization', 'ttl'],
    'Deduplicates concurrent misses for the same key and binds cached entries to explicit version or content keys.',
    'When a hot key expires or starts cold, 50 concurrent readers shouldn’t trigger 50 identical index/AST builds.',
    'Key caches by immutable revision/content_hash when possible, bound entry count (LRU), and coalesce misses per key.',
    {
      complexity: 'Turns O(N) duplicate concurrent misses on one key into 1 computation + N-1 shared waits.',
      tradeOffs: 'Dramatically cuts cold-start and spike load; if one caller’s context cancels, take care not to abort the shared computation for the other N-1 waiters.',
      failureModes: 'Caching mutable pointers lets one caller mutate the cached struct for all future readers; unbounded maps leak RAM.',
      code: 'if hit, ok := cache.Get(fileHash); ok {\n\treturn hit, nil\n}',
      watchOut: 'If singleflight uses the first caller’s short request ctx, its timeout cancels the shared call for every waiting caller.',
      selfCheck: { prompt: 'Why is keying an analysis cache by (path, sha256(content), policyVersion) superior to TTL expiration?', answer: 'Unchanged files stay valid forever without re-computation, while any byte or rule edit invalidates stale results instantaneously.' },
    }),

  // ── Algorithms & Data Structures ────────────────────────────────────────
  spec('amortized-complexity', 'Amortized O(1) vs hidden O(N²) loops', 'Design', 'Algorithms & Data Structures',
    ['amortized', 'quadratic', 'o(n^2)', 'preallocate', 'string builder', 'time complexity'],
    'Spots linear operations hidden inside loops (string +=, slice shift, repeated linear scan) that blow up on large repositories.',
    '1,000 items take 1ms at O(N) and 1s at O(N²); 20,000 files turn quadratic helpers into multi-minute hangs.',
    'Index repeated lookups in a map[K]V (O(1) expected), pre-size slices with make([]T, 0, n), and build strings with strings.Builder.',
    {
      complexity: 'O(N) single pass + hash index vs O(N·M) nested scan; strings.Builder copies each byte O(1) amortized times.',
      tradeOffs: 'A small map costs O(N) transient memory to drop CPU from quadratic to linear; for tiny fixed N ≤ 8, a flat slice scan beats map hashing.',
      failureModes: 's += line in a loop reallocates and copies all prior bytes on every iteration: 1 + 2 + ... + N = N(N+1)/2 copies.',
      code: 'byPath := make(map[string]File, len(files))\nfor _, f := range files {\n\tbyPath[f.Path] = f\n}',
      watchOut: 'slices.Contains inside a loop over files or hunks is O(N·M)—promote the inner list to a map[string]struct{} when N grows.',
      selfCheck: { prompt: 'Why is queue = queue[1:] in a breadth-first search risky in Go if elements hold large pointers?', answer: 'Slicing off the head is O(1) time, but the underlying array slots before head stay referenced until zeroed or reallocated, pinning memory.' },
    }),
  spec('graph-tarjan-dag', 'Dependency graphs, DAGs & cycle detection', 'Design', 'Algorithms & Data Structures',
    ['dag', 'topological sort', 'cycle detection', 'strongly connected', 'tarjan', 'bfs', 'dfs', 'graph'],
    'Models package imports, task stages, and symbol reachability as directed graphs to compute blast radius, ordering, and cycles.',
    'A clean architecture forms a Directed Acyclic Graph (DAG): leaf domain packages sit at sink nodes (out-degree 0), entry points at sources.',
    'Use 3-color DFS (unvisited, visiting, visited) or Tarjan SCCs in O(V + E) to detect cycles and compute transitive reachability.',
    {
      complexity: 'O(V + E) time and O(V) auxiliary state for BFS/DFS/topological sort and SCC decomposition.',
      tradeOffs: 'Adjacency lists (map[string][]string) keep sparse graphs compact; sort neighbor lists so traversal order is 100% deterministic.',
      failureModes: 'Recursive DFS without a visited/onStack set loops infinitely on cyclic user data or symlink/import cycles.',
      code: 'seen := map[string]bool{start: true}\nfor queue := []string{start}; len(queue) > 0; queue = queue[1:] {\n\tfor _, next := range edges[queue[0]] {\n\t\tif !seen[next] { seen[next] = true; queue = append(queue, next) }\n\t}\n}',
      watchOut: 'Iterating over a Go map randomizes order—sort roots and edges whenever graph output feeds UI, diffs, or hashes.',
      selfCheck: { prompt: 'In directed graph DFS, why does seeing an already-visited node NOT always mean a cycle?', answer: 'A diamond DAG (A→B→D, A→C→D) visits D twice across branches; a cycle exists only when an edge targets a node currently on the active recursion stack.' },
    }),
  spec('ast-tree-traversal', 'Syntax trees (AST) & structural analysis', 'Design', 'Algorithms & Data Structures',
    ['ast', 'tree-sitter', 'go/ast', 'parser', 'cyclomatic complexity', 'syntax tree'],
    'Parses source text into typed syntax nodes so symbol spans, imports, and cyclomatic complexity are exact rather than regex guesses.',
    'Regexes cannot count nested scopes, string literals, or comments reliably; an AST turns code into a tree of exact byte/line spans.',
    'Walk syntax trees iterative/visitor-style (ast.Inspect or tree-sitter cursor) and skip string/comment tokens by grammar node type.',
    {
      complexity: 'O(file bytes) single-pass parse + O(AST nodes) traversal.',
      tradeOffs: 'Full type-checking (go/types) resolves cross-package methods accurately but is 10–50x heavier than per-file syntax parsing (go/ast).',
      failureModes: 'Line-based regexes match commented-out imports or keywords inside multiline raw string literals (`...`).',
      code: 'ast.Inspect(file, func(n ast.Node) bool {\n\tif fn, ok := n.(*ast.FuncDecl); ok {\n\t\tvisitFunc(fset.Position(fn.Pos()).Line, fn)\n\t}\n\treturn true\n})',
      watchOut: 'Tree-sitter and go/ast byte offsets are UTF-8 byte indices—convert carefully when mapping to 1-based line or UTF-16 columns.',
      selfCheck: { prompt: 'What does cyclomatic complexity M = 1 + decision_points actually count?', answer: 'The number of linearly independent control-flow paths through a function—a lower bound on the branch cases needed to cover every path.' },
    }),
  spec('streaming-bounded-memory', 'Streaming I/O & bounded-memory pipelines', 'Design', 'Algorithms & Data Structures',
    ['streaming', 'io.reader', 'bufio.scanner', 'limitreader', 'bounded memory', 'backpressure io'],
    'Processes files, diffs, and subprocess logs incrementally in fixed-size buffers instead of slurping arbitrary inputs into RAM.',
    'Treat data size as adversarial: O(chunk) working memory handles a 5 KB or 5 GB log identically without OOM spikes.',
    'Compose io.Reader / io.Writer streams with io.LimitReader and bufio.Scanner (calling Scanner.Buffer when lines exceed 64 KiB).',
    {
      complexity: 'O(N) bytes streamed in O(bufferSize) constant memory.',
      tradeOffs: 'Streaming prevents OOMs and lowers GC heap targets, whereas os.ReadFile is simpler when a hard size cap is enforced upfront.',
      failureModes: 'bufio.Scanner silently stops on lines > 64 KiB unless you check scanner.Err() (bufio.ErrTooLong)!',
      code: 'sc := bufio.NewScanner(io.LimitReader(r, 8<<20))\nsc.Buffer(make([]byte, 64<<10), 1<<20)\nfor sc.Scan() {\n\thandleLine(sc.Text())\n}\nreturn sc.Err()',
      watchOut: 'Always check scanner.Err() after the Scan() loop—an I/O failure or >max-token line exits the loop cleanly with Scan() == false.',
      selfCheck: { prompt: 'What happens if bufio.Scanner hits a 100 KB minified JSON line with default settings and you omit scanner.Err()?', answer: 'Scan() returns false early and your code treats the truncated prefix as EOF with zero error!' },
    }),
  spec('prefix-tries-locality', 'Data locality, tries & compact representations', 'Design', 'Algorithms & Data Structures',
    ['cache locality', 'trie', 'prefix tree', 'binary search', 'sorted slice', 'memory layout'],
    'Aligns data layout with CPU cache lines (64 bytes) and prefix structure for fast path matching, fuzzy filtering, and tree views.',
    'A CPU L1 cache hit is ~1ns while main RAM is ~80ns: flat contiguous slices beat pointer-heavy node graphs by an order of magnitude.',
    'Keep sorted []string slices + slices.BinarySearch for static path prefixes, and pack frequently read fields together without pointer indirection.',
    {
      complexity: 'Binary search on sorted paths: O(log N · pathLen) with zero pointer allocations and contiguous cache prefetching.',
      tradeOffs: 'Pointer tries support O(keyLen) dynamic insert/prefix walk, while sorted flat slices use 5–10x less heap and zero GC pointer scans.',
      failureModes: 'Allocating millions of tiny heap-escaped node pointers stalls Go’s concurrent GC mark phase scanning pointer graphs.',
      code: 'idx, _ := slices.BinarySearch(sortedPaths, dir+"/")\nfor i := idx; i < len(sortedPaths) && strings.HasPrefix(sortedPaths[i], dir+"/"); i++ {\n\tyield(sortedPaths[i])\n}',
      watchOut: 'Struct field order matters: interleaving bool and int64 wastes padding bytes—group 8-byte fields ahead of 1-byte flags.',
      selfCheck: { prompt: 'Why does Go’s garbage collector care far more about pointer count than raw byte slice size?', answer: 'A 10 MB []byte has zero pointers so GC marks it in O(1); 10 MB of linked structs forces GC to chase and mark hundreds of thousands of pointers.' },
    }),

  // ── Architecture & Boundaries ───────────────────────────────────────────
  spec('consumer-interfaces', 'Small consumer-side interfaces', 'Design', 'Architecture & Boundaries',
    ['interface', 'small interface', 'dependency inversion', 'decoupling', 'accept interfaces'],
    'Defines narrow 1–3 method interfaces in the package that consumes a capability rather than exporting kitchen-sink provider interfaces.',
    'Dependency Inversion without ceremony: callers declare the minimum contract they actually need; concrete implementations stay decoupled.',
    'Accept narrow interfaces, return concrete structs; let tests pass simple in-memory fakes instead of brittle mock frameworks.',
    {
      complexity: 'Dynamic interface dispatch is ~2ns indirect call; may cause arguments to escape to heap if compiler cannot devirtualize.',
      tradeOffs: 'Decouples policy from transport/storage cleanly; over-abstracting internal helpers before a second caller exists adds indirection noise.',
      failureModes: 'Returning a typed nil pointer (*MyStore)(nil) as an interface produces a non-nil interface value (iface != nil is true!).',
      code: 'type TokenReader interface {\n\tReadToken(ctx context.Context) (string, error)\n}',
      watchOut: 'Watch for typed-nil traps: an interface holding (*MyErr)(nil) is non-nil (err != nil is true!).',
      selfCheck: { prompt: 'Why does var p *os.PathError = nil; var err error = p make err != nil evaluate to true?', answer: 'An interface value is (type, data); err holds (*os.PathError, nil), and an interface is nil only when BOTH its dynamic type and value are nil.' },
    }),
  spec('acyclic-dependencies-instability', 'Acyclic dependencies & Martin instability (I)', 'Design', 'Architecture & Boundaries',
    ['instability', 'afferent coupling', 'efferent coupling', 'stable dependencies', 'package boundary', 'coupling'],
    'Measures module fan-in (Ca) and fan-out (Ce) so volatile orchestrators depend on stable domain cores—never the reverse.',
    'Instability I = Ce / (Ca + Ce) ∈ [0, 1]: I → 0 is a heavily-depended-upon foundational core; I → 1 is an entry/orchestrator free to change.',
    'Enforce directional import rules in AGENTS.md / architecture tests: high-I packages (cmd, api, app) import low-I packages (project, storage).',
    {
      complexity: 'O(V + E) static import graph check at test/gate time.',
      tradeOffs: 'Prevents "big ball of mud" tangles where touching one file recompiles and breaks everything; requires extracting pure leaf types when two layers share concepts.',
      failureModes: 'Letting a foundational leaf (I ≈ 0) import a volatile orchestrator (I ≈ 1) makes the entire repository hard to test and change.',
      code: '// Stable Dependencies Principle: depend in the direction of lower instability\n// cmd/daemon (I=1.0) -> internal/api (I=0.5) -> internal/project (I=0.0)',
      watchOut: 'Breaking an import cycle by dumping types into a generic "common" or "util" package recreates coupling under a vaguer name.',
      selfCheck: { prompt: 'If package P is imported by 12 packages (Ca = 12) and imports 0 internal packages (Ce = 0), what is I and what does changing P imply?', answer: 'I = 0 / 12 = 0.0 (maximally stable core): P has zero internal dependencies to break it, but breaking P’s contract impacts 12 callers.' },
    }),
  spec('ports-adapters-leaf-packages', 'Hexagonal boundaries & pure leaf packages', 'Design', 'Architecture & Boundaries',
    ['hexagonal', 'ports and adapters', 'leaf package', 'domain boundary', 'clean architecture'],
    'Keeps domain rules, diff calculation, and syntax analysis pure and deterministic, pushing HTTP, SQLite, Git, and LLMs to adapters.',
    'Functional core, imperative shell: 90% of subtle logic becomes pure value-in → value-out code testable in milliseconds without I/O.',
    'Keep HTTP decoding in internal/api, orchestration/consent in internal/app, and pure project analysis in leaf packages.',
    {
      complexity: 'Zero I/O latency in unit tests; deterministic replay on identical inputs.',
      tradeOffs: 'Clear ownership and fast tests, traded against thin mapping DTOs at HTTP/storage boundaries so wire changes don’t leak inward.',
      failureModes: 'Passing *http.Request or SQL rows into domain logic forces every test to spin up servers/DBs and tangles policy with transport.',
      code: '// Handler decodes HTTP -> calls App service -> Domain computes pure plan\nplan, err := domain.ComputeImpact(snapshot, changedPaths)',
      watchOut: 'Handlers must never reach directly into storage or LLM clients, bypassing app-layer consent and revision guards.',
      selfCheck: { prompt: 'What is the fastest smell test for whether domain logic belongs in a pure leaf package?', answer: 'Ask: "Can I test every edge case of this logic with plain structs in memory, no filesystem, network, clock, or goroutines?"' },
    }),
  spec('state-machines-illegal-states', 'Explicit state machines vs boolean soup', 'Design', 'Architecture & Boundaries',
    ['state machine', 'discriminated union', 'illegal states unrepresentable', 'invariant', 'transition'],
    'Models workflows as explicit states and validated transitions rather than loosely coupled booleans that permit impossible combinations.',
    '4 booleans (isLoading, isDone, isFailed, isPaused) create 2^4 = 16 combinations when only 5 states are valid—11 are latent bugs!',
    'Use a single typed status enum/tagged struct and centralize legal transitions (e.g. running → paused | awaiting_review | failed).',
    {
      complexity: 'Reduces state space from O(2^k) boolean combinations to O(S) explicit, exhaustively testable states.',
      tradeOffs: 'Slightly more upfront type design eliminates entire classes of "button enabled while stale and running" UI/daemon bugs.',
      failureModes: 'Resetting err = nil on retry while forgetting to clear paused = true leaves a chimeric state no test ever anticipated.',
      code: 'switch session.State {\ncase StateDraft, StateChecked:\n\treturn allowApply(session)\ndefault:\n\treturn ErrInvalidTransition\n}',
      watchOut: 'Nullable optional fields alongside status flags drift out of sync unless transitions construct the whole next state.',
      selfCheck: { prompt: 'You see { loading: false, error: "timeout", data: oldData, stale: false }. How do you refactor it?', answer: 'Replace independent flags with a discriminated state union (idle | loading | ready{data, freshness} | failed{reason, lastGood?}).' },
    }),

  // ── Security & Trust ────────────────────────────────────────────────────
  spec('path-traversal-guard', 'Path traversal guard', 'Safety', 'Security & Trust',
    ['path traversal', 'filepath.clean', 'filepath.rel', 'directory traversal', 'symlink', 'path validation'],
    'Confines untrusted relative paths inside a workspace root so ../ segments, absolute paths, and symlinks cannot escape.',
    'Lexical cleaning is not enough: a clean path inside /workspace can still point to /etc/passwd through an on-disk symlink.',
    'Reject NULs/absolute paths, check filepath.Rel stays inside root, and use os.Root or filepath.EvalSymlinks before touching disk.',
    {
      complexity: 'O(path segments) lexical check + O(depth) kernel lstat traversal when resolving symlinks or openat2/os.Root.',
      tradeOffs: 'os.Root (kernel-confined openat) prevents symlink TOCTOU races between path validation and file open.',
      failureModes: 'strings.HasPrefix("/work/app-secret", "/work/app") returns true! Always enforce a path separator boundary or filepath.Rel.',
      code: 'rel, err := filepath.Rel(root, target)\nif err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {\n\treturn ErrOutsideRoot\n}',
      watchOut: 'filepath.Join("/app", "../secret") resolves to "/secret" outside /app — always check filepath.Rel after joining.',
      selfCheck: { prompt: 'Why is strings.HasPrefix(cleanTarget, cleanRoot) a classic security vulnerability?', answer: 'If root is "/work/repo", "/work/repo-secrets/keys.json" shares that string prefix while residing in a sibling directory outside root!' },
    }),
  spec('constant-time-secrets', 'Constant-time & salted secrets', 'Safety', 'Security & Trust',
    ['constant-time', 'constant time', 'subtle.constanttimecompare', 'salt', 'argon2', 'bcrypt', 'pbkdf2', 'kdf', 'hmac', 'crypto/rand'],
    'Eliminates byte-by-byte timing leaks on token/MAC checks and defeats precomputed rainbow tables with per-secret random salts.',
    'Standard == stops on the first mismatched byte (~nanoseconds difference), letting an attacker guess a token 1 byte at a time (256·N tries instead of 256^N).',
    'Generate nonces/tokens with crypto/rand, hash fixed-size digests when lengths vary, and compare with subtle.ConstantTimeCompare.',
    {
      complexity: 'Constant O(len) bitwise XOR accumulation independent of where bytes diverge.',
      tradeOffs: 'Negligible CPU cost for tokens/HMACs; password KDFs (Argon2id) intentionally trade memory/CPU (~50–200ms) to slow offline cracking.',
      failureModes: 'subtle.ConstantTimeCompare immediately returns 0 on unequal lengths, leaking exact secret length unless inputs are hashed first.',
      code: 'if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {\n\treturn ErrInvalidToken\n}',
      watchOut: 'subtle.ConstantTimeCompare returns 0 immediately when lengths differ — hash or check fixed-size digests first.',
      selfCheck: { prompt: 'How many guesses does an attacker need for a 32-byte token if byte-by-byte timing leaks perfectly vs constant-time?', answer: 'At most 32 × 256 = 8,192 guesses with a byte-by-byte timing oracle, versus 2^256 (astronomically infeasible) in constant time!' },
    }),
  spec('boundary-parse-dont-validate', 'Boundary validation & parse, don’t validate', 'Safety', 'Security & Trust',
    ['boundary validation', 'input validation', 'allowlist', 'json.decoder', 'disallowunknownfields', 'sanitization', 'parse dont validate'],
    'Transforms untrusted raw bytes at the edge into constrained domain types whose invariants hold everywhere downstream.',
    'Validation checks a condition and throws away the proof; parsing returns a narrower type so downstream code cannot forget the check.',
    'Cap request bodies with http.MaxBytesReader, reject unknown JSON fields on write DTOs, and construct validated value types at the edge.',
    {
      complexity: 'O(payload) bounded by explicit maxBytes ceiling (e.g. 1 MiB) before allocation.',
      tradeOffs: 'Strict DisallowUnknownFields catches client typos and stale fields immediately; versioned external webhooks may prefer explicit ignore.',
      failureModes: 'io.ReadAll(r.Body) without http.MaxBytesReader lets a single local/remote request allocate gigabytes and OOM the daemon.',
      code: 'r.Body = http.MaxBytesReader(w, r.Body, 1<<20)\ndec := json.NewDecoder(r.Body)\ndec.DisallowUnknownFields()\nreturn dec.Decode(&req)',
      watchOut: 'Avoid scattering partial string checks deep in domain helpers where callers assume invariants already hold.',
      selfCheck: { prompt: 'Why is http.MaxBytesReader preferred over io.LimitReader(r.Body, limit) in HTTP handlers?', answer: 'LimitReader silently truncates at limit (looking like EOF/malformed JSON), whereas MaxBytesReader returns a typed *http.MaxBytesError (413 Payload Too Large) and closes the connection.' },
    }),
  spec('capability-least-privilege', 'Capability tokens, loopback CSRF & sandboxes', 'Safety', 'Security & Trust',
    ['csrf', 'origin', 'loopback', 'capability', 'least privilege', 'sandbox', 'command injection'],
    'Protects local daemons and tool runners against cross-origin browser attacks, ambient authority abuse, and shell injection.',
    'Binding to 127.0.0.1 does NOT stop a malicious website open in the user’s browser from POSTing to localhost unless custom headers + tokens gate writes!',
    'Require a per-launch random token + custom header on loopback mutations, pass argv slices (never sh -c) to exec.CommandContext, and gate consent per scope.',
    {
      complexity: 'O(1) header + constant-time token check at HTTP middleware before route dispatch.',
      tradeOffs: 'Zero friction for the embedded UI while blocking DNS rebinding, cross-site form POSTs, and unapproved remote data egress.',
      failureModes: 'Passing user/LLM strings into exec.Command("sh", "-c", cmd) enables shell metacharacter injection (; ` $()).',
      code: '// Direct argv execution — no shell interpolation\ncmd := exec.CommandContext(ctx, "git", "diff", "--", cleanRelPath)',
      watchOut: 'Even without a shell, leading "-" in a filename looks like a git/cli flag unless preceded by "--"!',
      selfCheck: { prompt: 'Why include "--" before file paths in exec.CommandContext(ctx, "git", "checkout", "--", path)?', answer: 'Without "--", a file named "-f" or "--help" is parsed by git as a command-line option (argument injection) even though no shell is used.' },
    }),
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

function toGuide(entry: CatalogSpec): ConceptGuide {
  const { matches: _, ...guide } = entry;
  return { ...guide, curated: true };
}

/** All curated CS, System Design, and Go Systems concepts in curriculum order. */
export function allCuratedConcepts(): ConceptGuide[] {
  return CATALOG.map(toGuide);
}

/** Looks up a curated concept card by its stable Atlas slug id. */
export function lookupConceptById(id: string): ConceptGuide | undefined {
  const found = CATALOG.find(entry => entry.id === id);
  return found ? toGuide(found) : undefined;
}

/** Resolves a concept tag to a curated Go/CS/System Design card, or a clean project-pattern fallback. */
export function lookupConcept(raw: string): ConceptGuide {
  const key = normalize(raw);
  if (!key) {
    return {
      id: 'project-pattern',
      title: raw || 'Concept',
      category: 'Project pattern',
      domain: 'Architecture & Boundaries',
      what: 'A pattern surfaced in this change.',
      mentalModel: 'Trace where this pattern enters, what invariant it protects, and which callers depend on it.',
      inGo: 'Inspect the hunk beside this tag or ask how this codebase applies it across files.',
      curated: false,
    };
  }
  let best: { entry: CatalogSpec; score: number } | null = null;
  for (const entry of CATALOG) {
    if (entry.id === key.replace(/\s+/g, '-')) {
      best = { entry, score: 200 };
      break;
    }
    for (const candidate of entry.matches) {
      const target = normalize(candidate);
      let score = 0;
      if (key === target) score = 100 + target.length;
      else if (containsPhrase(key, target)) score = 60 + target.length;
      else if (key.length >= 4 && containsPhrase(target, key)) score = 40 + key.length;
      if (score > (best?.score ?? 0)) best = { entry, score };
    }
  }
  if (best) return toGuide(best.entry);
  return {
    id: `custom:${key.replace(/\s+/g, '-')}`,
    title: raw.trim(),
    category: 'Project pattern',
    domain: 'Architecture & Boundaries',
    what: `“${raw.trim()}” is an engineering pattern or domain idiom highlighted in this change.`,
    mentalModel: 'Domain-specific patterns encode local invariants—check where they are enforced and whether callers can bypass them.',
    inGo: 'Accepted tasks record every occurrence in your Learning log so you can trace how this project uses it.',
    curated: false,
  };
}

export interface DomainMastery {
  domain: AtlasDomainInfo;
  concepts: ConceptGuide[];
  metCount: number;
  totalCount: number;
}

export interface StudyCalibration {
  written: number;
  same: number;
  partly: number;
  missed: number;
  unrated: number;
}

export interface AtlasMasterySummary {
  totalCurated: number;
  metCurated: number;
  countsById: ReadonlyMap<string, number>;
  tagsById: ReadonlyMap<string, string>;
  uncategorizedTags: { name: string; count: number }[];
  domains: DomainMastery[];
  calibration: StudyCalibration;
}

/** Aggregates Learning Log entries into 7-domain CS/System Design coverage and Study-mode diagnostic calibration. */
export function computeAtlasMastery(lessons: Pick<Lesson, 'concepts' | 'own_plan' | 'own_match'>[]): AtlasMasterySummary {
  const countsById = new Map<string, number>();
  const tagsById = new Map<string, string>();
  const customCounts = new Map<string, number>();
  const calibration: StudyCalibration = { written: 0, same: 0, partly: 0, missed: 0, unrated: 0 };

  for (const lesson of lessons) {
    if (lesson.own_plan?.trim()) {
      calibration.written++;
      if (lesson.own_match === 'same') calibration.same++;
      else if (lesson.own_match === 'partly') calibration.partly++;
      else if (lesson.own_match === 'missed') calibration.missed++;
      else calibration.unrated++;
    }
    const seenInLesson = new Set<string>();
    for (const tag of lesson.concepts ?? []) {
      const resolved = lookupConcept(tag);
      if (resolved.curated) {
        if (!tagsById.has(resolved.id)) tagsById.set(resolved.id, tag);
        if (!seenInLesson.has(resolved.id)) {
          seenInLesson.add(resolved.id);
          countsById.set(resolved.id, (countsById.get(resolved.id) ?? 0) + 1);
        }
      } else if (tag.trim()) {
        customCounts.set(tag.trim(), (customCounts.get(tag.trim()) ?? 0) + 1);
      }
    }
  }

  const curated = allCuratedConcepts();
  const domains: DomainMastery[] = ATLAS_DOMAINS.map(domain => {
    const concepts = curated.filter(c => c.domain === domain.id);
    const metCount = concepts.filter(c => (countsById.get(c.id) ?? 0) > 0).length;
    return { domain, concepts, metCount, totalCount: concepts.length };
  });

  const uncategorizedTags = [...customCounts]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));

  return {
    totalCurated: curated.length,
    metCurated: countsById.size,
    countsById,
    tagsById,
    uncategorizedTags,
    domains,
    calibration,
  };
}

/** Question handed to Ask the code when exploring a concept in the active project. */
export function conceptQuestion(concept: string): string {
  return `Explain how this project uses ${concept}: where it appears, why it is used there, and what a newcomer should know about it.`;
}
