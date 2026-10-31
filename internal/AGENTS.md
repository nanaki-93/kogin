# Go daemon and evaluation code

Read [../AGENTS.md](../AGENTS.md) first. These rules cover `internal/` and are also
the backend guide for changes to `cmd/`.

## Boundaries and contracts

- Target Go 1.27.3; check `go.mod` before using newer language or library features.
  Keep production Go code in `cmd/` and `internal/`. Entry points wire dependencies;
  handlers decode/validate requests, call workflow owners and map errors.
- Reuse `internal/api` request decoding and structured errors. Preserve size
  limits, unknown-field/trailing-value rejection and local request policy. Use
  typed errors or `errors.Is`/`errors.As` where available, with useful `%w` context.
- Keep shared workflow policy in `app` and project facts/composition in `project`.
  Place domain-specific logic in its dedicated subpackage rather than expanding root
  packages (`internal/project/diff`, `internal/project/symbols`, `internal/project/insight`,
  `internal/project/handbook`, `internal/project/securityrules`;
  `internal/app/archgate`, `internal/app/sandbox`, `internal/app/changes`,
  `internal/app/gitops`, `internal/app/features`).
  Subpackages must respect the acyclic import rules declared in the root `AGENTS.md`
  and enforced by `internal/app/archgate`.
- Keep ADK imports in `internal/app/agentruntime`; pass admitted host operations
  into that leaf package. Its parent `app` retains consent, retries, source
  identity, publication and Apply/Undo authority. Keep SDK types out of wire DTOs
  and project facts. Completion compatibility uses one provider call per runner
  invocation, with private session history and no SDK-owned retry loop.
- For a wire-contract change, update Go request/response types, daemon route
  registrations, the Web UI wire types (`web/src/api/types.ts`) and its fake
  daemon (`web/e2e/support`), `docs/openapi.yaml` and `docs/api-contract.md` as
  applicable. Cover both successful and rejected requests in handler tests. Never silently relax guards for convenience.

## Identity, mutation and persistence

- Reuse project/path policy for containment, symlinks, exclusions and context
  limits; `filepath.Join` alone does not establish that a path is safe.
- Carry project ID/revision, file hash and proposal/run identity through asynchronous
  operations. Recheck before publishing results or changing source; a late result
  must not become evidence for a different project, file, proposal or run.
- Preserve the guarded Apply/Undo workflow in `app/change_apply_undo.go`. Apply consumes a
  stored, checked proposal identity, not arbitrary request source. Revising a proposal
  invalidates its previous checks; Undo requires the expected post-Apply hash.
- Use existing atomic storage primitives and domain stores. Preserve permissions,
  backup/audit behavior and useful persistence errors. A failure after a source
  write must not be reported as if no mutation occurred.
- When changing persisted data or cache meaning, handle existing versions
  deliberately. Invalidate incompatible evidence; keep required historical
  records readable. Change the relevant schema/prompt/policy identity when its
  semantics change, and cover migration or rejection with fixtures.
  Journal format 2 binds `runtime_family` to `adk-go-v2`; version 1 reads are
  passive and migration belongs to the first admitted store mutation. Restored
  checkpoints or approval receipts never recreate execution/check authority.

## Models, jobs and execution

- Use configured `analyze`, `bug` and `code` scopes through existing clients.
  Preserve scope-specific remote confirmation and context filtering. Treat model
  output as untrusted data: validate it before composition or publication.
- Whole-project analysis uses the existing admission and job lifecycle. Keep
  previews free of dispatch and result filters free of scope changes. Resume
  requires fresh admission; retain cumulative attempt accounting and partial
  coverage across interruptions.
- Give every goroutine/process an owner and bounded lifetime. Carry cancellation
  through synchronous request work; background jobs use their existing lifecycle
  owner. Preserve deadlines, request/output limits and inclusive retry budgets.
  Avoid holding locks during provider or subprocess I/O.
- Project-code checks require the existing execution-trust guard
  and isolated workspace. Use controlled argv, bounded diagnostics and process
  cleanup; never execute model text as a shell command. A user-operated
  terminal does not authorize model-driven commands.
- Keep source, secrets and transcripts out of metadata/logs that are defined as
  source-free. Preserve provenance and freshness instead of presenting advisory
  output as verified evidence.
- `internal/agenteval` and `cmd/agent-eval` own the synthetic task/trajectory
  corpus and source-free evaluation receipts. The bounded `app` entry point uses
  production ADK execution and repository tools without opening a checkout.
  Keep negative controls replay-only and independent regression inputs out of
  model prompts. Ordinary validation must never select live evaluation mode.
- `cmd/agent-inspect` reads journal metadata directly; private exports require an
  exact selection. It never constructs the daemon's running service.

## Verification

Follow the root Go validation gates. Start with the affected packages; extend
existing test fixtures and fakes rather than copying workflow setup. For changed
guards, include rejection/no-side-effect assertions. For asynchronous changes,
exercise cancellation, replacement and late completion using deterministic
synchronization. For persistence changes, exercise corrupt data and write failure.

Tests must use temporary projects and fake HTTP providers. Live evaluations,
runtime startup and model downloads are separate work requiring an explicit
request; building or testing the evaluation CLI does not require a campaign.
