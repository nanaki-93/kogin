# Mini-Orca local API contract

**Version:** 5.0.0
**Base URL:** `http://localhost:9090`  
**Content type:** `application/json`

Mini-Orca is a loopback daemon serving one active project to its embedded Web UI.
The shared change workflow captures explicit text paths (up to eight write paths for a planned task),
prepares versioned proposals, checks them, records diff review and explicitly
applies them with guarded recovery/Undo.
Native ADK execution records generation, test generation, verification,
specialist review and human input separately. Run events, artifacts and model
verdicts are evidence; they cannot replace current host admission or source
mutation authority. See [agent capabilities](../README.md#agent-capabilities)
and [journal migration and recovery](../CONFIG.md#local-metadata-and-manual-cleanup).
Source and composed diffs stay read-only. The daemon never writes source,
runs project code, commits or pushes merely because a project or history is opened.

`docs/openapi.yaml` is the machine-readable request/response contract. The
route test in `cmd/daemon/main_test.go` compares both this table and the OpenAPI
paths with the daemon registrations.

Each request body is one size-limited JSON object. Unknown fields, trailing
values, invalid enum values, and missing required revision guards are rejected
with the structured error response described below.

## Local request boundary

The daemon serves local clients of its loopback API; it does not provide
browser sessions or CORS access. Every request that can change state (any
method but `GET`, `HEAD` and `OPTIONS`) must carry the daemon's per-launch
write token in `X-Mini-Orca-Token`; otherwise it receives `401 Unauthorized`
before any route runs. Each launch creates a new token and saves it, with a
newline, to a file only the daemon's user can read (mode `0600`):
`MINI_ORCA_TOKEN_FILE` when set (an absolute path), otherwise `mini-orca/daemon-token`
under the user configuration directory (`~/Library/Application Support` on
macOS, `$XDG_CONFIG_HOME` or `~/.config` on Linux, `%AppData%` on Windows). The
Web UI document carries the token in a `mini-orca-write-token` meta tag; a page
from an earlier launch is told to reload. A web page in another origin can read
neither, so it cannot forge a write even when the Host and Origin checks pass.
Reads, health checks and event streams need no token.
With the default loopback bind, requests must use a `Host` of `localhost`,
`127.0.0.1`, or `::1` (with an optional port). Requests carrying an `Origin`
header are rejected unless that origin is the daemon itself and the request is a
Web UI call (marker header) or a Web UI asset load; CORS preflight requests
receive `403 Forbidden` with no CORS response headers. This prevents a web page from treating the daemon as
a browser API while preserving local tool requests, which send no
`Origin` header. The embedded Web UI is same-origin and marks every request with
`X-Mini-Orca-Web-UI: 1`.

`POST` and `PATCH` requests with a body must send `Content-Type:
application/json` (a charset parameter is permitted); other media types and a
missing content type receive `415 Unsupported Media Type` before a route
handler runs. Analysis controls carry the full run identity in their JSON body. `GET /health` remains available to
loopback health checks with no request body.

`MINI_ORCA_BIND_ADDRESS` can deliberately bind the daemon beyond loopback. In
that mode the Host restriction is relaxed so a configured native client can
reach it; writes still need the token and browser origins are still rejected,
but reads are unauthenticated. Restrict it with network controls; it is not suitable for public or
untrusted networks.

## Live routes

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Liveness and daemon version. |
| GET | `/status` | Daemon status and `shared_proposals` workflow identifier. |
| GET | `/ui` | Local Web UI (lite IDE): dashboard, explorer, guarded review, agent workflow, issues, docs, checks and Git merge review. |
| GET | `/ui/assets/{path}` | Embedded Web UI stylesheet and ES modules (`.css`/`.js` only); same strict CSP, no directory listing. |
| GET | `/api/models/current` | Non-secret `analyze`, `bug`, and `code` model catalog and `config_file`, the configuration file read (empty for built-in defaults). |
| GET | `/api/models/available` | Model choices from configured transports, Pi, Agy and Claude Code, with defaults and separate discovery statuses. |
| POST | `/api/projects/import` | Import the user-selected project and build deterministic project facts; the project overview is requested from the project's analyze model only when it is local or `confirm_remote_provider` is true. |
| POST | `/api/projects/restore` | Restore a previously imported local project without contacting the model. |
| GET | `/api/projects/current` | Read the selected project summary without changing the active project. |
| GET | `/api/projects/current/overview` | Read source-free metrics, structured analysis, optional engineering specification (`docs`), coverage, and finding counts for `project_revision`. |
| POST | `/api/projects/current/docs/refresh` | Start writing the docs' model-written parts again in the background with the analyze model: the summaries of components whose files' summaries changed, then the overview from all of them. Needs `project_id`/`project_revision`, and `confirm_remote_provider` for a remote model; 202 with the running refresh, 409 while another runs. The overview's `docs_refresh` reports it until it ends. |
| GET | `/api/projects/current/analysis/selection` | Read the file checklist and saved exclusions with project/revision guards; no dispatch. |
| POST | `/api/projects/current/analysis/selection` | Save excluded paths with project/revision and selection identity guards; no source changes. |
| POST | `/api/projects/current/analysis/preview` | Whole-project, read-only preflight with strict project/revision guards. |
| POST | `/api/projects/current/analysis/run` | Durably admit the previewed run with fresh provider and Security intent. |
| GET | `/api/projects/current/analysis/run` | Read progress with `project_id` and `project_revision`; 204 when absent. |
| POST | `/api/projects/current/analysis/run/control` | Pause, resume or cancel the exact run generation. |
| GET | `/api/projects/current/analysis/results` | Read Bugs, Performance or Security evidence using full run identity; optional captured-file filter. |
| GET | `/api/projects/current/findings` | List source-free verified findings and AI suggestions with provenance, filters, and freshness. |
| GET | `/api/projects/current/rules` | Test file patterns (conventions plus AGENTS.md `test_files`), the indexed AGENTS.md files and any rules block that could not be read. |
| PATCH | `/api/projects/current/findings/{findingID}` | Record an explicit user triage status for one finding. |
| GET | `/api/projects/current/findings/{findingID}/task` | Draft the task that fixes one issue for the composer: kind, title, description, the file and its conventional test in any language, criteria and the linked finding. Nothing is saved. |
| GET | `/api/projects/current/scan` | Read an explicitly started Go scan for `project_revision` from a temporary copied workspace. |
| POST | `/api/projects/current/scan` | Start an explicit Go parser/vet/test scan in a temporary copied workspace. |
| DELETE | `/api/projects/current/scan` | Cancel the active explicit Go scan. |
| GET | `/api/projects/current/events` | Stream Server-Sent Events (SSE) when analysis runs, scans, reindexing or change workflows change state; workflow events name the task, stage and status and carry the run's usage and estimated cost. |
| GET | `/api/projects/current/quarantine` | List records set aside because they were unreadable, and records skipped because a newer Mini-Orca wrote them, with the remedy. |
| GET | `/api/projects/current/usage` | Total agent model calls, tokens and estimated cost per task in the checkout, plus work not tied to a task. |
| GET | `/api/projects/current/agent-runs` | Read bounded root invocation history with project/revision guards and pagination. |
| GET | `/api/projects/current/agent-runs/events` | Replay source-free durable progress or subscribe using SSE and a sequence cursor. |
| GET | `/api/projects/current/agent-runs/{runID}` | Read invocation hierarchy and retained events; private model text requires `include_text=true`. |
| POST | `/api/projects/current/agent-runs/{runID}/control` | Cancel an active root invocation owned by this daemon. |
| POST | `/api/projects/current/agent-runs/{runID}/input` | Respond to a native checkpoint through the guarded proposal review owner. |
| POST | `/api/projects/current/agent-runs/{runID}/export` | Export one explicitly confirmed source-free trace snapshot to the configured collector. |
| GET | `/api/projects/current/setup` | The project's configuration as setup edits it: `state` (`needed`, `skipped`, `configured`, `invalid`), the project and daemon files, models (secrets as `api_key_set`), roles and budget. |
| POST | `/api/projects/current/setup` | Write the project's `.mini-orca/config.yaml` and apply it live; 409 while work runs. |
| POST | `/api/projects/current/setup/timeouts` | Save only the project's time limits and apply them at once. |
| POST | `/api/projects/current/setup/skip` | Keep the daemon's configuration for this project and stop offering setup. |
| POST | `/api/projects/current/setup/test-model` | Send one short request to a model as setup would configure it for a scope. |
| GET | `/api/projects/current/agent-settings` | Read configured or overridden role, budget and skill settings without dispatch. |
| POST | `/api/projects/current/agent-settings/control` | Confirm a versioned replacement or reset for future admissions. |
| POST | `/api/projects/current/agent-settings/skills/validate` | Explicitly validate a configured skill's captured local text without executing resources. |
| GET | `/api/projects/current/execution-trust` | Read remembered project trust bound to the current revision and exact project-code command scope; `task_test_name` selects a generated task-test argv. |
| POST | `/api/projects/current/execution-trust` | Save or revoke local execution trust for the current project path. |
| GET | `/api/projects/current/index` | Read deterministic eligible-file facts. |
| GET | `/api/projects/current/files/info` | Read safe selected-file information for project-relative `path`. |
| GET | `/api/projects/current/impact` | Read advisory source-free impact references for one file. |
| GET | `/api/projects/current/git/workspace` | Read local branches, worktrees and committed fix history. |
| POST | `/api/projects/current/git/worktrees` | Create a new branch and worktree from the displayed HEAD. |
| POST | `/api/projects/current/git/switch` | Switch a clean project to an existing local branch. |
| POST | `/api/projects/current/git/merge-preview` | Preview the selected worktree's incoming commits and diff. |
| POST | `/api/projects/current/git/merge` | Merge the captured worktree HEAD into the current clean branch, then remove the merged worktree and branch unless `keep_worktree`. The result names `previous_head`, the commit before the merge. |
| POST | `/api/projects/current/git/merge-checks` | Run the tests and lint of the subprojects a merge changed (`from` → `to`) on a copy of the merged checkout; failures that already happened before the merge pass as `known_failures`. Needs execution trust; never changes the checkout. |
| POST | `/api/projects/current/git/cleanup-merged` | Reconcile an external merge and safely remove the integrated worktree and branch. |
| POST | `/api/projects/current/git/discard-worktree` | Remove a confirmed clean worktree without merging; keep its branch unless the current branch contains its commits. |
| POST | `/api/projects/current/git/abort-merge` | Abort the identified merge and discard conflict resolutions. |
| GET | `/api/projects/current/git/repository` | Read guarded repository identity, branch and working-tree state. |
| POST | `/api/projects/current/changes/{sessionID}/commit` | Retry the latest failed proposal commit without reapplying source. |
| GET | `/api/projects/current/git` | Read target-file Git availability, branch, and status. |
| GET | `/api/projects/current/files/analysis` | Read cached semantic analysis for one selected file and revision. |
| POST | `/api/projects/current/files/analysis` | Explicitly analyze exactly one selected file. |
| POST | `/api/projects/current/files/security-scan` | Scan one selected eligible Go file with deterministic source-only rules; no provider request or subprocess execution occurs. Matches require syntactic review and do not prove exploitability. At most five source-order matches are returned; a `partial` report says when that limit truncates coverage. |
| POST | `/api/projects/current/files/explanation` | Explicitly explain one selected Go file using transient Code-scope context. |
| POST | `/api/projects/current/security-review` | Explicitly perform one passive, source-free AI Security review for one current eligible file. |
| POST | `/api/projects/current/reindex` | Refresh deterministic facts without an LLM request. |
| POST | `/api/projects/current/plans/investigate` | Investigate captured read/write scope and return validated citations and model suggestions for explicit acceptance. |
| GET | `/api/projects/current/changes` | List local change conversations without generation. |
| POST | `/api/projects/current/changes` | Capture a task and 1–64 explicit text paths (planned tasks: 1–8), here or with `worktree` in a new branch and worktree created for it. |
| GET | `/api/projects/current/changes/recovery` | Read the latest grouped mutation or interrupted recovery journal. |
| GET | `/api/projects/current/changes/{sessionID}` | Read a proposal and recheck its scoped freshness and whether its checks describe the current tree. |
| GET | `/api/projects/current/changes/{sessionID}/rules` | Measure each task file against the budgets and import rules of its AGENTS.md files (the proposal where the task changed it); nothing runs. |
| POST | `/api/projects/current/changes/{sessionID}/resume` | Restore history and clear check/review authority; no provider request. |
| POST | `/api/projects/current/changes/{sessionID}/messages` | Generate a scoped proposal or explicitly request bounded check-driven repair. |
| POST | `/api/projects/current/changes/{sessionID}/checks` | Check the current proposal; tests/vet require independent execution trust. |
| POST | `/api/projects/current/changes/{sessionID}/workflow` | Start creation, test writing/execution and model review for the captured scope. |
| POST | `/api/projects/current/changes/{sessionID}/workflow/cancel` | Cancel the identified workflow without applying source. |
| POST | `/api/projects/current/changes/{sessionID}/workflow/input` | Respond to the exact durable review request with current proposal identity. |
| POST | `/api/projects/current/changes/{sessionID}/plan` | Save revised task intent and scope before generation. |
| POST | `/api/projects/current/changes/{sessionID}/review-file` | Record inspection of a changed file for the current hash. |
| POST | `/api/projects/current/changes/{sessionID}/comments` | Add a reviewer comment on one line of the current proposal. |
| POST | `/api/projects/current/changes/{sessionID}/comments/delete` | Delete an open line comment. |
| POST | `/api/projects/current/changes/{sessionID}/review` | Record diff review for the current checked hash. |
| POST | `/api/projects/current/changes/{sessionID}/review-override` | Override the model reviewers' rejection of the current checked hash with a reason; approves nothing. |
| POST | `/api/projects/current/changes/{sessionID}/apply` | Explicitly apply the current reviewed proposal with grouped recovery; for a task that owns its worktree, accept it as a commit on its branch (no Undo). |
| POST | `/api/projects/current/changes/{sessionID}/delete` | Delete a task that is not running and whose Apply cannot be undone; stale tasks included. Publishes a `workflow` event with status `deleted`. |
| POST | `/api/projects/current/changes/{sessionID}/reanalyze` | Re-check an out-of-date task's issue against the current files: still valid captures it again, not valid deletes it and dismisses its linked issues. |
| POST | `/api/projects/current/changes/{sessionID}/undo` | Restore the latest unchanged grouped Apply or recover an interrupted mutation. |
| POST | `/api/projects/current/changes/{sessionID}/verify` | Explicitly verify the latest applied change with fresh identity and trust. |
| GET | `/api/projects/current/instructions` | Preview applicable AGENTS.md content, origins and instruction presets. |
| POST | `/api/projects/current/instructions/proposal` | Prepare an AGENTS.md proposal without a provider or source write. |
| GET | `/api/projects/current/features` | Read saved goals and advisory feature suggestions. |
| POST | `/api/projects/current/features/goals` | Save goals; ideas generated for other goals become stale. |
| POST | `/api/projects/current/features/generate` | Explicitly add new bounded feature ideas, keeping existing ones, with optional profile consent. |
| PATCH | `/api/projects/current/features/{featureID}` | Save, dismiss or reopen an advisory feature suggestion. |

Project analysis keeps the existing string fields: new `architecture` values contain
Mermaid flowcharts and `flows` items contain flowcharts or sequence diagrams. These are
advisory model interpretations. Older prose values remain supported. New `components`
items use `path (Module name): responsibility` for display; clients must retain readable
fallbacks for existing freeform strings.

Project import sends a strict `json_schema` response format for the description,
diagrams, string lists, risks and nullable engineering insights. Prompt version
`project-analysis-v5` specifies JSON escaping and the existing report limits;
earlier successful reports remain readable but are stale. Provider rejection of the structured
request does not trigger a fallback to unconstrained chat. Truncated or refused
replies cannot become fresh reports, even when their content parses as JSON.
Failed descriptions retain the factual inventory and persist a source-free failure
reason; restore reads that outcome without contacting the provider.

File explanation requests bind `project_id`, `project_revision`, `base_file_hash`, and `target_path`. The target must resolve to an eligible indexed Go file. A non-loopback HTTP or CLI Code provider also requires `confirm_remote_provider: true` on that request. The daemon rechecks the project and file identity after provider work before returning the bounded explanation, source line anchor, optional engineering insight, and `ContextManifest` provenance.

Explanation responses are transient. This route does not create chat sessions or drafts, persist chat or analysis history, run checks, or change Apply/Undo state. Cancellation, malformed provider output, and stale request identity return an error without publishing a result.

Security review requests require `project_id`, `project_revision`, `base_file_hash`, `path`, and `confirm_remote_provider`. The request uses the configured Analyze scope and requires fresh `confirm_remote_provider: true` before a non-loopback HTTP or CLI provider receives source. It sends at most 64 KiB of target source plus up to 32 KiB of applicable project instructions, and accepts at most 64 KiB of one strict JSON response with at most five advisory `model_suspicion` findings. The provider receives a strict `json_schema` response format with lowercase rule/category identifiers and file-specific path and line bounds. Provider rejection of structured output fails the stage without falling back to unconstrained output. Prompt version `security-file-v4` also requires source-free explanations and an empty findings array when no concrete security concern is supported, including documentation and ignore files. Earlier prompt versions remain readable but are stale for current reviews. The service rechecks project, revision, file hash, policy, and provider identity before delivery, after response, before caching, and before returning. Reports omit source, redact stored/returned prose, and reject meaningful source-line token sequences even when spacing or punctuation changes. Findings are advisory suspicions, and `completed_empty` means no findings were returned; it does not mean the file is secure. The route never runs suggested exploit code, scans, or project code; a configured CLI runs only as the model transport.

## Trusted local execution

Import, restore, navigation, indexing, parsing, and `gofmt`/`go vet` source analysis never execute imported project code. `go test` can execute package initialization and tests, including generated temporary task tests. Before it can run, the client reads the exact argv scope from `GET /api/projects/current/execution-trust`; pass the validated generated test name as `task_test_name` to receive its exact `-run` argv. The client then explicitly posts `confirm: true` for the active project revision. This choice is saved in private user settings for the canonical project path and remains separate from remote-model consent. Reopening or reindexing a trusted project validates its current sources and captures a new workspace fingerprint; old revisions and workspace hashes never authorize new jobs. Other project paths and worktrees require their own consent, and a changed command scope requires confirmation again. Posting `confirm: false` revokes saved trust. Invalid or unreadable settings fail closed, and persistence failures are returned as errors. Navigation and trust restoration do not execute project code or dispatch a provider. Commands run only in a temporary copied workspace with a small toolchain environment allowlist; this is trusted local execution, not sandboxing. On supported Unix platforms cancellation owns the command process group and its descendants. Other platforms reject project-code execution when that ownership is unavailable; source-only commands remain available.

## Project intelligence and limits

Import and reindex build deterministic metadata; neither starts a verified scan
or unified analysis. Indexing streams hashes and line counts for large files, while
import extraction is limited to text files within the 1 MiB source-view limit.
Larger text files retain full size, hash and line-count facts with a diagnostic
that source inspection is unavailable. Index schema version 4 rebuilds older
cached entries under this file-level extraction policy.
Go scans and semantic analysis are always user-started. A scan
prepares a bounded temporary copied workspace before parser, vet, and test phases;
a resource failure is returned as a failed `workspace` phase with sanitized evidence.
Findings keep source (`ai`, parser, vet, or test), confidence, severity, status,
and freshness separate so a model suggestion is never presented as a verified
tool result.

Shared proposals support captured Go and Markdown files. Go changes require
parse and format checks; other languages retain import extraction
and analysis without source editing.

## Privacy, configuration, and errors

The daemon binds to loopback by default. It filters ignored, generated,
configuration, and secret-like paths before assembling model context. Each
prompt request checks the effective scope shown by `/api/models/current`: project
import and explicit source-based Performance reviews use `analyze`, selected-file analysis uses `bug`, and
code proposals use `code`. A non-loopback HTTP or CLI scope requires
`confirm_remote_provider: true` for that request only; confirmation for one
scope never authorizes another. Restore, reindex, scans, validation, checks,
Apply, and Undo never require provider confirmation.

Context policy reads `.gitignore` and `.mini-orcaignore` in each traversed
directory. Patterns use directory-relative Git ignore semantics, including
anchored paths, directory exclusions, `**`, and ordered negation; an excluded
parent must be reopened before a child can be included. Explicit context-policy
include/exclude overrides retain their precedence, but cannot include secret,
generated, metadata, or symlink paths. Nested ignore changes participate in the
policy fingerprint. Policy version 2 makes evidence captured under the previous
ignore interpretation stale; refreshing it requires the usual explicit analysis.

Configuration is local-only in `config.yaml` (ignored by Git); begin with
`config.example.yaml`. The API never returns configured credentials. All API
failures use a structured error object with `type`, `message`, and
`user_message`. Project paths are canonical project-relative paths and revision/hash
guards return `409 Conflict` when their captured base is no longer current.

`model_scopes` is loaded only when the daemon starts, and all fixed scopes are
required. A changed model, provider, or reasoning effort makes old AI cache
entries stale. Providers can use OpenAI-compatible Chat Completions or native
Google `gemini`/`vertex` adapters. The ADK boundary supports typed function calls
and bounded text streams; existing HTTP proposal operations still return only
completed domain results. Native Anthropic Messages, OpenAI Responses, HTTP model
enumeration and credential-vault features are not exposed. Optional scope
`reasoning_effort` is safe metadata and maps to the selected provider's supported
controls. Keys and Vertex access tokens belong only in ignored local config and
are neither logged nor returned.

Configured model metadata includes `capabilities`: boolean `completion`,
`json_schema`, `tools` and `streaming`. These describe implemented adapter
protocols, not an account's model entitlements. CLI adapters support completion
and locally validated JSON Schema only. HTTP and native Google adapters also
support tools and text streaming. Unsupported combinations remain false. Historical metadata may omit the object; clients
must treat missing capabilities as unavailable. `/api/models/current` remains
passive and sends no provider request. Native model names are configured explicitly;
available-model discovery remains the existing explicit Agy/Pi catalog operation.
See [native Google configuration](../CONFIG.md#native-google-providers).

Scopes can instead select `provider: agy`, `provider: pi` or `provider: claude`
(Claude Code) in local configuration. These CLI transports feed the same analysis
and proposal workflows; they add no command-execution API. Catalog and provenance
identify them as `cli://agy`, `cli://pi` or `cli://claude`, always with
`remote_provider: true` and the same scope-specific confirmation requirement.
Catalog selections take the forms `agy:<slug>`, `pi:<provider>/<model>` and
`claude:<alias>`; `claude` discovery is `ready` when the executable is installed
and lists its aliases (`opus`, `sonnet`, `haiku`, `fable`). Each CLI runs on the
subscription it signed in with; Claude Code runs without API-key environment
variables or user settings, and a run that would still bill an API key is
rejected before the model is called. CLI `temperature` and `max_tokens` metadata are
`null` (unavailable), while HTTP values remain numeric. Executable paths and
CLI credentials are not exposed. CLI responses must complete successfully;
structured responses are also validated locally before entering domain parsers.
See [CLI configuration and boundaries](../CONFIG.md#cli-providers).

Prompt-bearing HTTP provider requests never follow redirects. Mini-Orca rejects
every 3xx response at the configured provider origin and returns a status-only
error; it does not send the prompt, request body, or authorization header to a
redirect target, and it does not retry a rejected redirect. Update the configured
provider URL explicitly when its endpoint changes.

## Unified analysis contract

These routes are registered through the same loopback/origin policy as the other
local APIs. The shared controller owns unified analysis execution.
All bodies reject unknown fields and trailing JSON. Query guards must each occur
once, use only documented names and contain 1–4096 characters. Missing/invalid
request fields return 400, unknown projects 404, identity/lifecycle conflicts 409,
and progress storage failures 500 with sanitized recovery guidance. No saved run
returns 204. Result `path` must be a canonical path captured by this run; an
unknown file yields 409. Filtering leaves the run's project coverage unchanged.

- `POST /api/projects/current/analysis/preview`: accepts `AnalysisPreviewRequest`;
  returns `AnalysisRunPreview` without provider calls, subprocesses or source writes.
- `POST /api/projects/current/analysis/run`: accepts `AnalysisRunStartRequest`;
  returns an admitted `AnalysisRun` with status 202 after durable admission. When
  `clear_results: true` is paired with `refresh: true`, admission clears saved
  findings, stage reports and feature suggestions (preserving saved project goals)
  and replaces any paused or interrupted run so the new run starts from 0.
- `GET /api/projects/current/analysis/run`: reads current progress for the required
  `project_id` and `project_revision`; it never starts or resumes work.
- `POST /api/projects/current/analysis/run/control`: accepts
  `AnalysisRunControlRequest` for pause, resume or cancel.
- `GET /api/projects/current/analysis/results`: accepts the complete run identity
  as query guards plus `category` and optional project-relative `path`; returns
  `AnalysisSectionResults`. A file filter only changes the read view.

There is one execution scope: `project`. No selected file, path filter or list of
user-picked files belongs in a start request. Preview captures the whole eligible
project inventory, sorted by canonical path, with content hashes, language, size,
stage eligibility/cache dispositions and explicit exclusions. The 100-file default
and 500-file maximum bound a dispatch window, not the inventory or total coverage.
Default window time is 900 seconds, maximum 3600. Total attempts per model stage
include the initial request and transport retries: default 2, maximum 4. Counters
never reset on resume/restart. Cached work and passive Security rules make zero
model requests; retries cannot be multiplied by an undisclosed outer retry loop.

Four per-file stage identities are fixed: `semantic`, `performance`, `security_rules`,
`security_ai`. Semantic risks can feed all three result categories. Performance
reviews feed Performance; Security rules and advisory reviews feed Security.
The latter three retain their existing report types and provenance. A stage's
consumers do not classify its prose. Stage work counts once in request budgets,
even when it feeds several result sections. The preview exposes expected model
requests without retries and the inclusive maximum for the remaining work.

`GET /api/models/available` returns `models[]` (`id`, `name`, `provider`,
`source`, `location`, `model`), `defaults` mapping the three legacy scopes to
configured IDs, and `pi`/`agy: {status, message?}`. Configured choices are deduplicated
by model/transport/sampling configuration, independently of operation. Pi entries
include every authenticated/custom model reported by `get_available_models`,
including local models from Pi's configuration. Pi discovery disables
tools/extensions/MCP/context loading, sessions and startup network refreshes.
Agy entries use the exact slug and display name returned by `agy --log-file
/dev/null models` (tab-separated stdout in Agy 1.3.4), including the CLI's model
variants. Agy may contact its catalog service; no static model list is used.
Each CLI uses a private working directory and a 20-second deadline. Neither
receives a prompt or project context. No query parameters are accepted. Either
CLI's failure retains configured choices and the other CLI's models with an
explicit `unavailable` status for the failed discovery; a successful empty list
is `ready`.
Only safe metadata is returned, never authentication headers, full endpoint URLs
or executable paths. `location` is `local` for loopback catalog destinations,
`remote` for other nonempty destinations, and `unknown` when Pi omits the URL.
Agy reports `unknown` because its catalog omits endpoints. This describes catalog
metadata; all CLI prompt requests still require consent.

Preview/start accept optional `models: {code, performance, security, features}`. Each value is
an opaque catalog ID (`configured:<hash>`, `pi:<provider>/<model>` or `agy:<slug>`), or a legacy
configured profile name (`analyze`, `bug`, `function`); all four operations need a choice when
the object is present. Older clients and saved runs may supply `review` as the
fallback for either omitted `performance` or `security` choice. Explicit operation
choices take precedence; legacy captured identities remain unchanged. Omission retains Bug for semantic Code analysis and Analyze
for Performance, Security AI and feature discovery. Catalog assignments use the
operation's context and dispatch limits; legacy profile selections retain their
original timeout/retry behavior. Configured choices retain their transport and
sampling settings; discovered CLI choices use that CLI's defaults and exact model
ID, without inheriting a configured scope's reasoning override. Logical scope remains
Bug for Code and Analyze for reviews/features. Clients cannot supply endpoints,
executable paths or credentials. Choices affect dispatch, cache freshness,
provenance and provider accounting without changing daemon configuration.
Preview and start must use identical choices. Invalid syntax returns 400; a
missing choice or changed admission returns 409 before inference. CLI membership
is rechecked before preview, start, resume and standalone feature generation;
discovery failure stops admission. Captured choices survive restart/resume.
Schema-1 runs without `models` remain readable with their original defaults.

Preview/start accept `include_features` (default false for existing clients),
which must match at admission. The web client includes it on every new analysis.
It adds one project-wide `feature_suggestions` step using saved goals, bounded
policy-filtered context and root AGENTS.md; explicit file exclusions also apply
to this context and suggested paths. `preview.features` captures goals/workspace
hashes, the current feature-store hash, exclusions and the selected feature provider/request
allowance. `run.features` exposes its status, cumulative attempts and nullable
`suggestion_count`, separately from the three finding sections. The step uses the
same additive feature generator as the Features routes: `suggestion_count` (0–5)
counts ideas newly added by this run's generation, not the cumulative library,
and `completed_empty` means no new ideas were added. A full feature history
records the step as `failed` with `Feature history is full; existing ideas were
kept.`; it leaves the saved ideas unchanged and is not a run persistence fault.
The step runs once
alongside file stages with its own deadline of at least ten minutes (or the
selected feature model's timeout when longer). File batch/time limits do not consume
its deadline. It retains the run's cumulative attempt allowance and fresh
provider consent, and is not repeated after completion on resume. A batch that
finishes or reaches its file allowance can still publish file results while
feature discovery runs. The window settles after both workers finish; user
pause/cancel stops both workers. Failed generation keeps
previous ideas and permits other stages to continue; overall progress reflects
partial results. Cancellation and source/goal changes reject late publication.
Saved schema-1 runs without feature fields retain their original behavior.
The feature prompt identity (`feature-suggestions-v4`) is part of the provider
fingerprint, so finished runs that included features under an earlier prompt
read as `stale`, not corrupt.

Every preview also plans the docs refresh that ends a run (`preview.docs`): the
components the docs show, the analyze provider and at most one request per
component plus one for the overview, each with its retries. These requests are
not in the preview totals, and the analyze provider needs the run's consent even
when no file stage uses it. After the last window's files and feature discovery,
components whose files' summaries changed are summarized again; the overview is
written again from the component summaries when one changed or it is not current
for the revision. `run.docs` records the outcome (`completed`, `skipped` or
`failed`, with counts and a sanitized reason); a failed refresh keeps the
summaries already written and does not change the run's status. The plan is not
part of `queue_id`, so runs saved before it resume; they end without a refresh.
`POST /api/projects/current/docs/refresh` does the same outside a run, in the
background, and always writes the overview again; it stops when the project
changes or the daemon shuts down, keeping the summaries already written.

`queue_id` binds project/revision, policy, provider fingerprints, ordered file/hash
and stage identities, exclusions, refresh choice and limits. Effective provider
identity includes scope, model, origin, reasoning, context/timeout/retry settings
and prompt/rule versions. Cache availability is excluded from this stable identity:
the run's own cache writes cannot invalidate its remaining queue. `preview_id`
additionally binds current cache dispositions, remaining attempts/work and request
bounds. Start echoes limits/refresh, `retry_stale_failed`, `stale_only`, `stale_path`, `recover_incomplete`, `include_features`, `models` and both identities; the daemon recomputes
them before admission. A changed preflight yields 409 and requires a fresh preview.

Preview and start accept optional `retry_stale_failed` (default false). When true,
only eligible files with stale or failed producer evidence, durable failed stages,
or stages a canceled or stopped run left unfinished are selected (so canceling a
retry keeps its work due). A failed refresh overrides older cached evidence and is retried even when an
older report remains available. A later saved review supersedes that failure.
Fresh stages reuse their caches; files with no prior analysis are
retained, not analyzed, unless another stage on that file qualifies it. Missing, partial and
ineligible stages alone do not qualify a file. This option cannot be
combined with `refresh`. The selection is part of both admission fingerprints;
resume echoes the option and retains the admitted file set even after reports
become fresh. Excluded files remain accounted for in the inventory. An empty
selection produces no model requests.

Scoped runs (`retry_stale_failed`, `stale_only`, `stale_path`, `recover_incomplete`)
still cover the whole project, so a retry never drops earlier results. Every
eligible file the scope does not select stays in `files` with `retained: true`
and `max_model_requests: 0` on each stage: stages with a current saved report
are read back from it, and the others keep their earlier outcome in `carried`
(`failed`, `stale`, `skipped` for never-analyzed, `canceled` or `unavailable`)
with its reason. Retained files are never dispatched.

In every run, each stage the plan reuses (`cached: true`) is settled from its
saved report when the run is admitted: it is reported completed (with
`cached: true` and its finding count) from the first read, does not count
toward `batch_files`, and pause, cancel or interruption cannot change it. Only
stages that need a model or a rules scan are queued. Run status and
section coverage therefore describe the project, not only the retried files.
Runs saved before retention excluded those files with a scope reason; they are
still read, resumed and reported as before.

Preview and start accept `stale_only` (default false) to select just files with
stale evidence. Unrelated failed or missing files are retained (see scoped runs
below); current stages stay cached. This option cannot be combined with `refresh`, `retry_stale_failed`,
`recover_incomplete` or `include_features`. It is bound to both admission
fingerprints. Resume uses the captured option and file set; requesting it for a
run without that option returns 409.

With `stale_only`, optional `stale_path` narrows analysis to one exact
project-relative path in the eligible saved selection. Invalid paths or using a
target without `stale_only` return 400; unknown, excluded or ineligible targets
return 409. A target without stale evidence yields an empty preview. The target
is captured in the preview and both fingerprints; start must echo it and cannot
drop or change it. Resume preserves the captured target when omitted and rejects
a different target with 409. This does not change the saved file selection.
Other files are retained with their own freshness, while user and policy
exclusions remain excluded.

Preview and start also accept optional `recover_incomplete` (default false), which
completes unfinished analysis without changing source code. Under the requested
`models`, it selects files with at least one recoverable eligible stage: stale,
failed, partial, missing, canceled, interrupted or unavailable evidence whose model
is configured (passive Security rules need none). Other eligible files are
retained; user, policy and unsupported-file exclusions keep their own reasons. Recoverable stages bypass
caches within the attempt limits, current successful stages remain cached with
no requests, and stages whose model is not configured keep that reason with no
requests. Pending, running and paused work belongs to its run, so paused and
interrupted runs still require continuation. The option returns 400 when combined
with `refresh`, `retry_stale_failed` or `include_features`, so a recovery plan
never contains a feature step. It is part of the stored plan and both admission
fingerprints. On resume the captured plan decides it: clients may omit it, but
sending `true` for a run admitted without it returns 409. Resume retains the
admitted file set. An empty recovery selection produces no model requests.
`retry_stale_failed` keeps the narrower semantics above for existing clients.

Unfiltered result reads include the saved evidence of retained files.
`retained_files` lists their identities (for runs saved before retention, the
current identities of files omitted only by the scope); user and source-policy
exclusions remain excluded.

File reports retain their original project revision as provenance. Their freshness
depends on that file's current content, applicable instructions, policy and analyzer
configuration. Editing another file does not invalidate them. A saved run may be
stale as an execution snapshot while its unchanged files remain current; result
reads still require that exact saved run identity and the active project.

Resume first requests a new preview with `resume_run` identifying the existing
run. It preserves that run's captured scope, limits and cumulative attempt counts,
but recalculates remaining request bounds. Control echoes the run identity,
fresh `preview_id` and fresh confirmations. The durable initial `plan` retains the
original source-free admission evidence; it never contains usable confirmation.
Run `id` plus `generation` prevents late results or controls from acting on a
replacement run with the same queue. All identity mismatches return 409. Unknown
request fields/enums, missing guards or invalid limits return 400.

Provider confirmation is a list of the displayed effective provider IDs. Security
AI additionally requires explicit `security_review: true`, including for a local
provider. This intent is limited to the displayed admitted work; it is consumed
when start/resume is attempted, is never saved in a run, and cannot survive restart
or authorize a changed provider/project. Pause/cancel do not carry confirmations.
Scope-specific existing consent rules remain enforced before each provider dispatch.
The run never invokes tests, vet, benchmarks, exploits or suggested shell commands.

### Retired workflows

The Kotlin desktop, declaration chat/drafts, declaration Apply/Undo and benchmark
comparison, Analyze-all jobs and separate Performance jobs have been removed.
Their former routes return 404. Use `changes` for checked proposals and grouped
Apply/Undo, and `analysis/preview`, `analysis/run` and `analysis/run/control` for
analysis. Read-only declaration explanations remain supported.

Version 5 removed the terminal UI and the features only it exposed: missions,
live sessions, cloud copies, remote agents and delegations, the inbound agent
listener, MCP and OpenAPI integrations with their API actions, grounded research,
ambient triggers, attachments, the artifact library and curated memory. Their
routes return 404 and their configuration keys fail startup. Agent journals that
still hold delegation or external-action records open unchanged; those records
are preserved but no longer used.

Existing shared proposals and unified runs retain their schemas and guards.
Single-stage journals marked `compatibility_stage` as semantic or performance
are ignored: opening a project neither resumes them nor grants consent. Their
bytes remain untouched until an explicitly started unified run replaces
`analysis/run.json`. Old files under `sessions/` and declaration backups are no
longer read or used for Undo. Matching per-file analysis remains reusable.

### Progress and partial evidence

Run and section states are `queued`, `running`, `pausing`, `paused`, `canceling`,
`canceled`, `interrupted`, `completed`, `completed_empty`, `partial`, `failed`,
`unavailable`, `stale`. File-stage states additionally identify `pending`,
`skipped` and incomplete producer reports as `partial`; paused work stays pending.
Each of Bugs, Performance and Security has its own progress entry. Analysis is
the progress owner; result prose belongs only to the three result reads.

Coverage counts captured file-stage units relevant to the section: total, pending,
running, succeeded, partial, failed, skipped and unavailable. These sum to total;
excluded files are separately visible in the plan. Ineligible stages are marked
`skipped` with a reason and omitted from section totals. Cached current evidence counts
as succeeded. A partial/truncated report retains findings but cannot claim full
coverage. Pending means unfinished, including abandoned work in canceled/stale
runs; it is not permission to dispatch. A successful stage shared by sections is
counted in each section's coverage, but only once in request/attempt budgets.

`finding_count` is JSON null until there is successful or partial evidence. Zero
means that such evidence returned no findings, not that the project is safe.
`completed_empty` requires nonzero, fully successful coverage and zero findings;
`completed` requires fully successful coverage with findings. Performance and
Security accept an empty findings array (including a bare `[]`) or blank final
content as no findings. Blank provider content requires an explicit normal
completion; refusals, truncation, missing completions and malformed nonempty
findings remain failures. An empty review is not proof of safety or performance.
Performance sends a strict `json_schema` response format with required finding
fields and file-bounded line numbers. The model supplies line ranges only;
the app derives an unambiguous innermost enclosing declaration from the index.
Ranges spanning declarations remain valid file/line findings without a symbol
label. Legacy responses containing a symbol still require that declaration to
contain the complete range; invalid labels are never silently discarded. Provider
rejection fails the stage without falling back to unconstrained output.
The Performance prompt is `performance-file-v7`; older saved reports remain
readable but require refresh for current coverage. Failed Performance stages
retain source-free explanations for malformed JSON, invalid fields or values,
invalid anchors, and oversized responses. A terminal `partial`
section has useful evidence and incomplete coverage, with no work still pending
or running. An all-failed/unavailable section cannot claim zero findings. Stale
counts are historical and must not contribute to fresh navigation badges.

Performance and Security review freshness uses the requested `configured_model`,
while `model` preserves the provider's returned name, which may be a resolved
alias or version. A different returned name alone does not make a completed
review stale. Older Performance reports omit `configured_model` and reuse `model`
for this check; if it differs from the current selection, explicit recovery must
refresh that review. Source, revision, policy and provider checks still apply.

Result reads also return nullable `saved_finding_count`, the total of the returned
classified semantic, Performance and Security findings. It includes retained stale
reports and is independent of `progress.finding_count`, which still measures the
current run. Unfinished, failed or canceled stages do not hide their last saved
reports. A successful producer review replaces that file's previous report,
including a successful empty review; repeated reads and retries do not add old
totals to new ones. Stale reports retain their original revision/hash and stale
labels. Missing saved evidence remains null, and unclassified risks are excluded
from this total. File-filtered reads count only their returned findings. These
totals are read from producer stores after restart; no run-schema migration is
needed.

Overall completion requires all eligible file-stage units to be completely covered. A mixed
terminal outcome with useful evidence is partial; no successful evidence yields
failed or unavailable. Reaching a window file/time/attempt budget with work pending
pauses the run with an explicit reason. Pause stops at a stage boundary; cancel
stops active requests and future dispatch while retaining completed reports.
Resume applies to paused/interrupted runs. Canceled or stale runs require a new
explicit start. Restart restores interrupted progress and never dispatches work.
Restoring or switching projects interrupts active work without making saved
results stale. Reindexing unchanged files preserves both results and active work;
freshness follows captured source contents and inventory, policy, and provider
identity, not timestamps or app sessions. Reading finished runs checks the live
source revision without rewriting the index; additions or removals of files
excluded by context policy do not invalidate their saved progress or file results.
Start/resume retain the complete admission inventory and execution workspace guards.
Finished results incorrectly marked stale by older restore/reindex behavior
recover in memory when their source, policy and provider identities still match,
including runs with ineligible skipped stages. Changed inputs and unfinished stale
work remain stale.
Window counters reset only on an admitted resume; total elapsed/attempt counters
and completed reports remain. Persistence failure stops dispatch before the next
stage and exposes a recoverable operational failure.

### Category and cache compatibility

`Finding.title` carries a short content-specific name. New semantic output
(`file-analysis-v16`) requests a title for every risk; Performance
(`performance-file-v7`) and AI Security (`security-file-v4`) request the same
2–6 word names, at most 60 characters. File risks retain these names in
`UnifiedFinding.title`; older risks without a title use their existing summary.
The additive field leaves historical reports readable and finding IDs/triage
unchanged. Reading saved results never generates titles with a provider call.

`Finding.category` and `UnifiedFinding.category` use `bugs`, `performance` or
`security`. They are independent of severity, confidence, source and the existing
specialized Performance/Security subtype categories. Storage envelope version 1
remains readable: the category field is additive and omitted on historical records.
Absent categories stay unclassified; no keyword inference, default Bugs category
or history rewrite occurs. Such evidence is available as previous analysis and
under `unclassified`, excluded from fresh section counts. General suggestions
remain file explanations and are not findings in any result category.

New semantic model output requires explicit categories and uses the updated prompt
identity introduced by ANA-02. Prompt-sensitive cache lookup returns older reports
as stale while preserving their full explanations and risks. Historical output is
not treated as classified. Unknown nonempty categories are rejected on writes;
absence remains accepted for legacy producers.
Finding IDs intentionally exclude category as well as revision/hash, so adding
or correcting a category preserves existing dismissed/fixed triage for unchanged
evidence. Typed Security triage/verification and Performance hypotheses are retained;
results do not merge unrelated producers merely because line/title text matches.

### Analysis file selection

`GET /api/projects/current/analysis/selection?project_id=…&project_revision=…`
returns `project_id`, `project_revision`, `selection_id`, `excluded_paths`,
`editable`, and `files` (`path`, `reason`, `stages`). An empty reason means the
file is eligible for selection. The checklist uses the project inventory, omitting build,
dependency and metadata directories; source-policy exclusions and unsupported
files remain visible with a reason and cannot be enabled by the checklist.

`POST` on the same route accepts `project_id`, `project_revision`, `selection_id`,
and a required `excluded_paths` array (an empty array selects all eligible files).
Paths must be unique canonical project-relative paths, up to 20,000 entries.
New exclusions must occur in the inventory; previously saved exclusions for
absent files may be retained. Stale project/selection guards and unknown new paths
return 409. Malformed input returns 400. An active, paused or interrupted run
must finish or be canceled before editing its selection (409 otherwise).

Exclusions are saved atomically in `.mini-orca/analysis/selection.json`, schema
version `1`, and restored across project/app/daemon restarts. Corrupt or unreadable
selection metadata fails explicitly; it never silently selects everything.
New files default to selected. Selection changes invalidate pending admission
through the existing preview/queue identity. The checklist scopes new unified
project analysis runs, including stale/failed retries and incomplete-work recovery;
it does not erase existing results or change explicit single-file actions or the
shared context policy.

Each selectable file's `stages` contains `stage`, `status`, and an explanation in
`reason`. Status reads inspect current source hashes, cache freshness and retained
run progress without model calls, using that run's captured model choices when
its revision still matches. Fresh cache evidence is required for `fresh`;
historic completion alone is insufficient. Changed unindexed files are `stale`.
Missing, failed, partial, unavailable, skipped, pending, paused, canceled and
interrupted analysis remain distinct. Unreadable file/cache evidence is reported
as unavailable with a reason. New unindexed files remain visible with a prompt
to refresh project facts. Ineligible files have an empty stage list and the
file-level exclusion reason. Selection for the next run does not erase saved
analysis status. The client classifies paths in `excluded_paths` as excluded,
just like files with a policy exclusion reason, and omits both from freshness
counts. Retained stage evidence becomes visible again when a file is re-selected.

Both selection responses also include `recovery: {state, file_count, stage_count,
reason?}`, the daemon's read-only answer to whether `recover_incomplete` can finish
saved-selection work now. Clients present it instead of re-deriving eligibility.
`state` is the first match of: `running` (an analysis run is active), `continuation`
(the last run is paused, interrupted or has a retained persistence fault; continue
or cancel it), `not_started` (no saved run for this project), `blocked` (a selected
eligible file changed since indexing or cannot be read; refresh project files),
`available` (at least one recoverable stage), `blocked` (the only unfinished work
needs an unconfigured model profile, which the reason names) and `complete`.
Recoverable stages follow the `recover_incomplete` rule above, observed with the
same model choices as the stage statuses. The counts cover selected eligible files
and recoverable stages under those choices, whatever the state; saved exclusions,
policy exclusions and unsupported files never count. A POST response reflects the
newly saved exclusions. `reason` is present for `running`, `continuation` and
`blocked`.

Overview `analysis_coverage` counts the same selected, eligible files as the file
checklist across applicable stages. Config and saved selection exclusions do not
contribute to coverage. `partial` retains incomplete, paused, interrupted and
canceled files; `unavailable` retains unreadable or unavailable evidence. Captured
run coverage and the project description retain their own historical status.

## Shared AI proposals and project instructions

All new GET routes require `project_id` and `project_revision` query guards.
The instructions preview also requires a canonical relative `path` ending in
`AGENTS.md`. It reads root-to-directory guides, respecting context exclusions
and rejecting symlinks. More local guidance applies within its own directory.
Applicable guides are included by default in project and file analysis, performance
and security AI reviews, declaration explanations, feature suggestions,
and proposal creation, testing and review. They grant no provider, execution or
Apply authority. AI file reports carry optional `instructions_fingerprint`
provenance; changed applicable guides make cached reports stale and invalidate
in-flight results. Older reports without this identity can only remain current
when no applicable guide exists and their other cache identities still match.
The preview's `presets` contain independent guidelines organized by `category`
into common AGENTS.md sections: project overview/architecture, build/development,
code style, testing, security/data, UI/accessibility, documentation and handoff.
These are organizational conventions, not mandatory AGENTS.md fields.
Choices match indexed languages, build manifests, UI files and supported imports
in the selected directory and descendants. This includes Go, Node/TypeScript,
Python, JVM, Rust, shell, Markdown, Go HTTP/database/CLI libraries, React imports
in TypeScript, desktop host manifests, Dockerfiles and GitHub Actions workflows.
Common project rules are always available. Matched entries carry `reason` and `evidence`
(up to three sorted indexed paths); general entries omit these fields. Current
context exclusions and binary-file filtering apply to the indexed evidence.
Reindex to refresh file facts. Loading these choices makes no provider request,
executes no project code and does not write AGENTS.md.

A change creation body carries `project_id`, `project_revision`, `kind`
(`fix`, `performance`, `security`, `feature`, `instructions`), `title`, `paths` and optional
`acceptance_criteria` and `plan`. Source paths must satisfy the context and write
policies; creation is supported, while deletion is unavailable. A path that does
not exist yet is captured as a new file. Code prompts list the exact captured
write paths, marking new files. A proposal, from a model or `changes` in a
message body, that writes any other path (including read context), empties a
file or is not UTF-8 text is rejected with an error naming the path; a captured
path spelled with `./` or repeated separators counts as that path. A
`/messages` request makes its model call within the request: closing the
request cancels the call and saves nothing. A manual instruction proposal adds
`content` to this body and requires exactly one AGENTS.md path. It makes no model call.

A session stays current while the files it depends on are unchanged, not the
whole project. Its freshness scope is the captured write and read paths (by
content, with their AGENTS.md instructions) plus `scope`: sorted `{path, hash}`
records of the other Go files in each target package, the non-test Go files of
local packages the targets import, and the build manifests (`go.mod`, `go.sum`,
`package.json` and lock files, `Cargo.toml`, `pyproject.toml`, `Makefile` and
similar) in the target directories and their ancestors. Files that `.gitignore`
or `.mini-orcaignore` exclude are not part of the scope, and at most 512 files
are recorded. A scope file that changes, appears or disappears makes the session
`stale`; GET returns `stale_reason` naming it, and the task list reports the same
freshness. Edits elsewhere and reindexing leave the session `current`; mutations
may then carry either the captured `project_revision` or the current one, and
checks run under the current execution trust. `scope_version: 1` marks such
sessions. Sessions captured before it have no scope and keep the whole-tree
fingerprint and their captured revision.

`POST /api/projects/current/changes/{sessionID}/reanalyze` re-checks a stale
draft. The body names the version shown (`project_id`, `revision`, `hash`;
`project_revision` is not checked, as for delete) and `confirm_remote_provider`
for a remote Bug model. The daemon captures the task again from the current
files (its title, kind, plan, criteria, write and read paths and finding
targets; read context that was deleted or is now excluded, and plan citations
that no longer match their source, are dropped) and
sends one Bug-model request with that context and the task's intent, asking
whether the problem is still present. The answer is a model suggestion
(`reason`, at most 1024 bytes). `outcome: still_valid` stores the recaptured
task under the same ID at the next revision: no proposal, checks, approval,
comments, plan approval or workflow, and one assistant message recording the
decision; the agents can then run. `outcome: not_valid` deletes the task, as
delete would, and dismisses its finding targets that belong to this checkout
or, for a task in its own worktree, to the checkout that created it;
`dismissed_issues` counts them and `warnings` reports a dismissal that failed.
A current task returns 400; running agents and an Apply that can still be undone
return 409, as does a task or source file that changed before the answer was
saved. Nothing is written when the request fails or is canceled.

The whole-tree fingerprint still guards checks. Every check copy must equal the
tree it was copied from and hold the captured sources and scope, and the
session records `checks_workspace_hash`, the tree its checks describe. When the
project changes after checks ran, GET returns `checks_outdated: true`, and
approval, a review override and Apply are refused until checks run again on the
current tree; the session itself stays current.

Unreadable records do not block a checkout. An agent journal that cannot be
read (invalid JSON, unexpected content, inconsistent records, or not a private
file of at most 64 MiB) is set aside when it is next opened: its bytes move to
`journal.quarantined-<UTC time>.json` beside it, the move is recorded, and the
next run starts a new journal. Agent history and waiting reviews it held are
no longer available. An unreadable task file is set aside the same way
(`<id>.quarantined-<UTC time>.json`) by the task list; listings that decide on
the whole history, such as merging a worktree, still fail and name the file.
Unreadable saved analysis progress (`.mini-orca/analysis/run.json`, for example
from an older Mini-Orca) is set aside to `run.quarantined-<UTC time>.json`
beside it when it is next read, so the overview loads and a new analysis can
be previewed and started. The moves of task files and analysis progress are
recorded in `.mini-orca/quarantine.json`; agent journals keep their own record.
Records written by a newer Mini-Orca (a higher journal `version`, task
`schema_version` or analysis progress `schema_version`) are never moved: the
agent journal refuses work with an error naming the file and asking to
upgrade, newer task files are skipped, and the overview loads without newer
analysis progress while an analysis preview returns `409` asking to upgrade.
The change recovery journal (`.mini-orca/changes/last-mutation.json`) is never
set aside, since it may describe an unfinished Apply; its errors name the file
and the remedy. `GET /api/projects/current/quarantine` (with the project
guards) returns `files` (set aside: `kind` `agent_journal`, `task` or `analysis_run`, `file`,
`quarantined_as`, `reason`, `remedy`, `at`) and `skipped` (newer files with the
same fields but no move).

Each `update` event of `/api/projects/current/events` carries a JSON `Event`:
`kind` (`workflow`, `project_index`, `analysis`, `findings`, `features`,
`scan`, `agent_settings` or `agent_run`) and, for workflows, `session_id`,
`workflow_id`, the task `title`, the `stage` that moved (`create`, `test`,
`review`, `human_review`; empty when the run ended), the workflow `status`, and
the run's `usage` and `cost` so far. Clients reload only the named task.

A workflow charges what its budget spent to the stage that was running, across
attempts: each stage of `workflow.stages` has `usage` and, on read, a `cost`
estimate. `GET /api/projects/current/usage` (with the project guards) totals the
agent journal per task: runs started for a task, including its manual
revisions, carry its session ID. Each row has `session_id`, `title`, `kind`,
`state`, `runs` (top-level runs), `usage` and `cost`; `other` covers analysis,
explanations and plans, and `total` everything. Costs come from
`agent_runtime.telemetry.prices`: `status: estimated` with a `total` when every
rate is known, otherwise `unknown` with the `known_subtotal` and `reasons`.

Line comments are the reviewer's requests on specific lines. `POST
.../comments` takes `ChangeIdentity`, a changed `path`, `side` (`new` for added
and context lines, `old` for removed lines), a `line` number on that side and
`text` (1–1024 bytes). The daemon quotes the line as `excerpt` and records the
proposal `hash`; comments change no source, revision or review state. A session
keeps at most 32 open and 128 comments, and the open ones fit a 6 KiB brief.
`.../comments/delete` takes `ChangeIdentity` and an open comment `id`. A
`/messages` or `/workflow` request with `include_comments: true` sends every
open comment as a numbered repair brief (path, line, quoted line and text; a
comment on an earlier version says so) after the optional message, which may
then be empty, and marks them `sent_revision`. A workflow request with its
brief stays within 8192 bytes. Without open comments the flag is rejected.

Apply, Undo, Git merges, merge aborts, branch switches and reindexing stop only
the running workflows whose tasks they made stale, by the same check: captured
sources, scope and, for path-scoped sessions, the checkout branch (commits on
it do not matter). A stopped workflow reads `canceled` with a reason naming the
event and the changed file; the others keep running under the revision their
run was admitted with, and a waiting human input stays answerable after a
reindex. Opening another project, removing a worktree and shutdown stop every
workflow.

The client supplies a `plan` with `problem`, `evidence`, `approach`, `non_goals`
and optional `read_paths`, plus at least one acceptance criterion. Creation captures
1–8 write paths and up to 8 distinct existing read-only paths. Read context cannot
become a write target. `plan.hash` binds the intent, criteria, source, instructions
and workspace identity. Saving a plan makes no provider request and runs no checks.
`POST /api/projects/current/changes/{sessionID}/plan` accepts `ChangeIdentity`,
`plan`, `paths` and `acceptance_criteria` while the draft has no proposal. It
recaptures the scope, advances the revision and clears plan approval. Once a proposal
exists, changing its intent requires a new plan. Missing scope is never filled from
an arbitrary index entry; oversized scope must be split explicitly by the user.
Optional plan `citations` (at most eight) contain `path`, captured `hash`, one-based
`start_line` / `end_line`, exact `quote` (at most 1024 UTF-8 bytes), and model
`observation` (at most 512 bytes). Citations must match captured read/write source.
Optional `assumptions` contain at most eight nonempty strings of at most 512 bytes.
Both fields participate in approval identity; adding or changing them clears
approval. Empty fields preserve existing plan hashes.

`POST /api/projects/current/plans/investigate` accepts `project_id`,
`project_revision`, `plan`, one to eight explicit write `paths`, optional initial
`acceptance_criteria`, `sources`, and `confirm_remote_provider`. Each selected
read/write path needs exactly one source record: `{path, hash, exists}`. Existing
files require the current indexed hash; new write paths require `exists: false`
and an empty hash. Read context must exist and remain distinct from write scope.
The optional `worktree_path` query selects an existing worktree without creating
one. Investigation creates private run evidence but no change session, execution
trust, source mutation or worktree.

The Code profile performs bounded read-only investigation with admitted
repository tools when supported; completion-only profiles receive captured
source instead. Remote profiles require explicit confirmation. Source, absence,
revision, policy and applicable instructions are checked before dispatch and
again before publication. Changed source returns a conflict. Invalid input or
unusable model evidence returns an error; neither replaces the editable draft.
Cancel the HTTP request to stop investigation.

The result contains `version: "v1"`, project identity, `input_hash`,
`context_manifest`, suggested `approach`, `evidence`, `assumptions`,
`suggested_scope`, and nonempty `acceptance_criteria`. Evidence quotes and hashes
are validated against captured source; observations and assumptions remain model
suggestions. Response limits are 32 KiB, eight citations, eight assumptions, eight
suggested paths, sixteen criteria (512 bytes each), and a 4096-byte approach.
Captured source is capped at 256 KiB; the Code profile's context limit also
applies. Suggested paths never extend admitted scope. Accepting suggestions in
the client updates the draft only; explicit plan approval is still required before
generation.

Legacy API conversations and schema-1 history without `plan` retain their direct
message flow. Planned sessions reject generation without current approval.

`POST /api/projects/current/changes/{sessionID}/review-file` accepts `ChangeIdentity`
and a changed `path`. It records `reviewed_files` and `file_review_hash` without
model requests, checks or source writes. Planned sessions require all changed files
reviewed at the current proposal hash before Review/Apply. A complete file review
captures `last_reviewed_hash` and `last_reviewed_changes`; `review_diff` compares
that snapshot with the current proposal, including files returned to original
source. Revising a proposal clears current review progress. Per-file `summary` and
one-based `criteria` references are advisory explanations, not verified coverage.

Finding-driven changes also capture optional `finding_targets` (up to sixteen
distinct entries). Each records the original `project_id`, producer `kind`
(`semantic`, `performance`, or `security`), finding `id` and project-relative `path`
within the captured paths. Security targets also require `source` (`ai` or
`deterministic`); other producers omit it. These immutable references remain in
the session and history when the task is prepared in another worktree. Existing
schema-1 sessions without references remain readable; titles and paths alone are
never used to infer which findings a historical commit addressed.

Messages, checks, review, Apply and Undo carry `project_id`, `project_revision`,
`revision` and `hash`; the session ID is in the route. Messages add `message`,
`confirm_remote_provider`, optional `profile` (`configured:<hash>`,
`pi:<provider>/<model>`, `agy:<slug>`, or legacy `analyze`, `bug`, `function`,
default `function`) and optional `repair`. Consent applies to the selected profile. Repair requires current failed
checks and consumes one of three durable attempts. Checks accept `run_tests` and
`run_lint`; once requested those checks remain required. Go is formatted before
proposal publication where parseable. Unchanged targets and Go rewrites that only
differ in formatting are omitted from the proposal. File diffs align unchanged
lines between separate edits; whitespace inside literals and Markdown remains
visible when it changes. Source parsing/formatting checks are always
required; code tests/vet run only with current execution trust in a copied workspace
using fixed argv. New test files are tested against the base and candidate, and a
passing candidate tests stay pinned through repair. For new Go tests, the
optional baseline observation reports `outcome: failed_before_change`,
`already_passes`, or `could_not_execute`. A build failure is not evidence of bug
reproduction. Existing baseline and candidate tests remain required; the new-test
observation does not replace them. A failing existing baseline predates the
proposal, so the workflow stops without automatic repair and says so. The
workflow's own verification runs tests and lint (`go vet` or the declared check
target), matching an explicit check with both options. Executed checks include `duration_ms`. Passing parser checks alone
does not establish runtime correctness. Apply and Undo require `confirm: true`.

Apply optionally accepts `commit_message` (non-empty, at most 4096 UTF-8 bytes),
`head` and `branch` to apply and commit the reviewed paths. Git-backed sessions
capture branch/HEAD in addition to source identity; a branch switch or external
commit invalidates the proposal. Git mutations require opening the repository or
worktree root on a local branch with an initial commit and no merge/rebase in progress.
Existing schema-1 history without Git identity remains readable; current request
Git identity and existing source guards still apply when committing it.

The commit uses a private index, raw reviewed blobs, `git commit-tree` and a
compare-and-swap branch update. Unrelated staged/untracked content is excluded;
pre-existing edits or staging on proposal paths block Apply & Commit before any
source writes. This operation skips hooks, attribute filters and signing. Git
identity comes from normal Git configuration. No push or remote request occurs.
The optional `commit` object on mutation receipts, sessions and history records
`state` (`pending`, `failed`, `committed`), message, original branch/HEAD, candidate
`oid` and any error. A failed commit leaves source applied. Retry via
`POST /api/projects/current/changes/{sessionID}/commit` carries current project
identity, proposal hash, `confirm: true` and an editable `commit_message` and
rechecks source and Git identity. A recorded candidate is reconciled after an
interruption before retry is offered; ambiguous outcomes stay pending.
Undo restores source as working-tree changes and does not rewrite commit history.

Every proposal revision clears earlier checks/review. Apply uses current-session
authority and rechecks source, policy, instruction and whole check-workspace hashes.
Local `.mini-orca/changes/` history intentionally contains source, proposed contents
and conversations in private files; it is excluded from prompt context and Git
source indexing. Limits are eight paths, 256 KiB prompt/response content, forty
messages, 2 MiB per conversation/recovery journal and two hundred conversations.
History lists return summaries (`id`, project identity, kind, title, revision,
hash, state, freshness and updated time); selected reads/resume return full contents.
History reads are passive. Resume clears check/review authority; stale history is readable but cannot
be applied.

`POST /api/projects/current/changes/{sessionID}/workflow` starts a bounded agent
workflow and returns HTTP 202 with the captured session and `workflow.id`.
The request carries the current `ChangeIdentity`, `message`, and
optional `models: {create, test, review}`. Omitted or empty role fields use
`agent_runtime.roles` (Creation `bug`, Testing `function`, Review `analyze` by
default). The returned workflow always contains all three resolved choices.
Each explicit model value is an opaque catalog ID
(`configured:<hash>`, `pi:<provider>/<model>`, or `agy:<slug>`) or a legacy
configured profile name (`analyze`, `bug`, or `function`); catalog membership is
rechecked before admission. Resolved defaults require the same consent as explicit
choices. `confirmed_profiles` lists the selected remote
profiles the user approved; local profiles need no remote confirmation. Security
tasks additionally require `confirm_security: true`. Current project execution
trust and at least one captured test path are required before dispatch.
The workflow supports features, fixes, performance and security work; instruction
editing keeps its existing manual proposal flow.

The daemon executes a native ADK workflow graph with stable `create`,
`generate_tests`, `verify`, `review`, and `human_review` nodes. Generation and
test writing use the selected models; verification retains the existing baseline
and candidate checks with controlled commands. The current four public stage
summaries remain compatible: Testing spans test generation and verification.
Private graph events contain node state and captured identities; model content
remains in private completion sessions. Each node start is persisted before its
host operation, with no SDK-owned node retry. Cancellation, invalid predecessor
identity or journal failure stops successor nodes. Restored graph events cannot
authorize or replay source mutations.

Review fans out to correctness and test-quality specialists, plus architecture
for Go or multiple source files. Security tasks add a security specialist only
after explicit security intent; performance tasks add performance advice labeled
`unmeasured`. Routing is frozen at admission. Every specialist uses the selected
Review profile and the same checked proposal, within the shared concurrency and
call limits. `workflow.specialists` retains each role's state, model provenance,
evidence label and completed report. Legacy records may omit this field.
Reports join in canonical role order, deduplicating findings and notes. Findings
are blocking: a report approves exactly when it has none. Optional `notes` are
advisory suggestions that never change a verdict. Each report has at most 16
findings and 8 notes; the aggregate has at most 80 and 40. A missing, failed or
canceled reviewer prevents aggregate approval. Parallel cancellation caused by a failed
reviewer remains a failed run rather than appearing as a user cancellation.

The testing agent may change only captured test
paths; its edits merge with the implementation. Checks use the isolated workspace
and existing regression baseline rules. Tests and review never apply source.

For a task in its own worktree whose create or test model is a pi, Claude Code or
agy profile, that stage runs as a tool-using agent instead of returning file
contents. Mini-Orca serves the run's tools over MCP on a loopback port with a
one-time bearer token; the CLI starts in a private directory with no other
extensions, MCP servers, built-in tools, skills or context files, a call to any
other tool stops the run, and its model can call only `read_file`, `list_files`, `search`,
`outline`, `find_definitions`, `find_references`, `edit_file`, `create_file`,
`delete_file`, `rename_file`, `run_checks` and `finish`. Reads follow the context
policy, writes the task's diff policy; the testing agent writes test files only
(and so needs no test path in the task's scope). A run has budgets for tool
calls (150 for the coder, 80 for the tester), changed files and check runs, and a
turn limit; its time limit is four model timeouts plus two focused check timeouts.
The worktree's diff becomes the next proposal version, the agent's `finish`
report its conversation message, and `workflow.stages[i].tools` records its calls,
the calls per tool, its turns and whether it finished. A run that fails before
`finish` returns the worktree to the stored proposal. Every model turn is charged
to the stage's usage. Reviewers also receive the proposal as a unified diff.
There is one active change workflow per conversation. Each model call uses the
largest of `timeouts.workflow_model_seconds` (900 seconds by default), the
generation timeout and its selected scope's timeout; retries share that deadline.
The review stage allows enough time for reviewers queued by the concurrency limit.
The total deadline is at least 30 minutes and includes remaining model budgets,
check/preparation allowances and enabled automatic repairs. Shared
`agent_runtime` call, token, concurrency and elapsed limits also apply across
stages and retries; exhausted budgets stop dispatch without applying source.
Failed checks
stop before review. A reviewer returning `changes_requested` blocks human approval
until a new run produces a passing proposal and approving review, or until the
human overrides that verdict.

`POST /api/projects/current/changes/{sessionID}/review-override` requires
`ChangeIdentity`, `workflow_id` and a `reason` (1–1024 bytes). It is accepted only
for a finished workflow in `changes_requested` whose complete specialist verdict
covers the current hash, with live passing checks and no human input; a human
rejection cannot be overridden. It records `workflow.override` (`proposal_hash`,
`reason`, `at`), keeps the model verdict unchanged and moves the workflow to
`awaiting_human_review`. It approves nothing: viewed files, passing checks and an
explicit `/review` are still required, and a new proposal or run discards it.

For planned sessions, workflow admission requires `plan_hash` matching the
captured plan. Approval does not bypass provider consent, execution trust or stale
source checks. `auto_repair` defaults to false; opting in permits at most two
automatic repairs through a conditional ADK graph. Each repair reserves the
shared repair budget and persists its transition before another generation.
Only actionable failed checks or completed rejecting reviews qualify; authority,
timeout, cancellation and persistence failures never cause automatic regeneration.
Checked-change workflows never add targets on their own. A proposal that
writes outside the task's files fails and names that file; capture it in a new
task to change it.
Interrupted or unavailable required checks stop automatic repair: they do not
establish a code failure. Their evidence remains available for an explicit retry.
Timeout reasons distinguish a model-stage deadline, a focused check deadline and
the total workflow deadline, with the relevant configuration setting where applicable.

A CLI that reports its subscription plan's usage limit is not retried. The
workflow stops with status `paused` (the stage that reached the limit, and its
running reviewers, read `paused`) and `workflow.pause` names the `stage`, the
`provider` whose plan reached its limit, `resumes_at` when the CLI said when the
limit resets, and the `resume_from` that continues the run: the stage itself,
`checks` for a review whose check authority did not survive, or `create` when
the testing stage has no proposal to test. Nothing the stage left half done is
kept, and its usage is charged. Start the workflow again with that
`resume_from`, possibly with another model for the stage; a pause is dropped
once the run leaves the `paused` status.

`resume_from` accepts `create` (default), `test`, `checks` or `review`. Test-stage
retry reuses the current implementation. A `checks` start keeps the whole current
proposal — implementation and tests, including a version revised by hand or in
the conversation — calls no writing model, runs the checks as the test stage and
then the reviewers; an automatic repair after it starts from creation. It
refuses `include_comments`, which would mark the comments sent unread. Critic
retry requires live, passing check authority for the exact current proposal and
reuses those checks without running generation or tests. Keep the model
selections for reused stages (creation for `test` and `checks`, creation and
testing for `review`); changing their models requires restarting from that
stage. Restored historical checks cannot authorize that shortcut.

`stream_output` defaults to false. Explicitly enabling it captures the choice in
workflow history and requests incremental text from every selected model role.
All selected providers must support streaming at admission; completion-only CLI
profiles are rejected before dispatch. Partial output remains advisory, bounded
private detail and cannot become a proposal until the final response validates.
Attempts, stage `started_at`/`finished_at`, check duration, failure reason and
repair mode are exposed to clients. Timing is diagnostic, not a benchmark.

Poll the existing guarded session GET for `workflow.status`, ordered `stages`,
captured model metadata, and the review's `proposal_hash`, `verdict`, `summary`
and `findings`. Status distinguishes `running`, `canceling`, `failed`, `canceled`,
`stale`, `interrupted`, `outdated`, `changes_requested`,
`awaiting_human_review` and `paused`. The final human stage waits for explicit diff review;
model approval never sets `reviewed_hash`. Existing Review/Apply/Undo routes retain
their identity and hash guards. Changing a proposal invalidates workflow review.

Approving critic results create a native ADK human-input checkpoint, exposed as
`workflow.input`. It includes a unique `request_id`, private run ID, workflow/node
identity, the `proposal-review-v1` schema, fixed `review_proposal` action, exact
`ChangeIdentity`, captured path/source/proposal hashes in `scope`, and `expires_at`
24 hours after creation. Its `status` is `waiting`, `approved`, `rejected`,
`canceled`, or `expired`; completed decisions include `received_at`. Waiting
releases the worker and preserves the checkpoint across restart. Legacy workflows
may omit `input`.

`POST /api/projects/current/changes/{sessionID}/workflow/input` requires
`ChangeIdentity`, `workflow_id`, `request_id`, and `decision` (`approve`, `reject`,
or `cancel`). The daemon revalidates captured source, proposal, scope and current
project/configuration/policy binding. Approval additionally requires live passing
checks and the existing plan/file review guards. Existing `/review` records the
same explicit approval receipt and accepts optional `notes` (at most 2048 bytes),
returned as `review_notes`; notes are cleared with the approval. The receipt is durable before native graph resume;
responses invoke no providers, checks, source writes or Git operations. Apply
remains a separate explicit action.

Repeating the same decision retains its original receipt; a conflicting decision
is rejected. A first response after expiry records `expired`, blocks review, and
requires a new workflow. Reject and cancel also block later approval of that
request; rerunning checks cannot override them. Storage failure leaves authority
ungranted and permits an idempotent retry. SDK session values and forged tool
responses cannot establish domain approval.

`POST /api/projects/current/changes/{sessionID}/workflow/cancel` takes current
`project_id`, `project_revision` and `workflow_id`, so stale controls cannot stop
a replacement run. It also records cancellation of a waiting input request.
Project replacement, reindex and daemon shutdown cancel the
worker. Background work can continue while the client navigates or disconnects.

### Agent invocation history and progress

Explicit project import, feature generation, declaration explanation, standalone
semantic analysis and security review have native knowledge runs. Provider
attempts appear as children and share the host budget; host retries, source
checks and publication authority are unchanged. Import binds its run to the
newly scanned checkout. A provider timeout/refusal retains a failed report and
rechecked factual inventory; journal or policy failures remain errors. Cached
reads, restore and handbook composition make no provider request.

Derived project handbooks use composition schema `2`. Component paths must exist
in the inventory; dependencies come from indexed imports. Model explanations,
diagrams and risks are advisory, and absent scenario steps, guards or decisions
are not invented. Older schema `1` handbooks are rebuilt in memory from retained
reports and indexes on read, without rewriting the saved history. Historical
project-analysis report schemas remain readable.

All agent-history reads require the current `project_id` and `project_revision`;
the existing optional `worktree_path` selects a registered checkout. Unknown or
duplicate query fields are rejected. Reads are passive and return `no-store`.

`GET /agent-runs` (under `/api/projects/current`) lists roots newest first, with
`limit` from 1 to 100 (default 50), an opaque `cursor`, and `next_cursor` when more
remain. A cursor is bound to its project/revision. Older-revision records are
marked `stale`; records with no live daemon owner are `interrupted`, never
advertised as running or cancelable. Native `parent_id`/`root_id` group attempts,
parallel reviewers, model invocations and human input. Budgets are shared totals,
so child usage must not be added together.
`usage.input_tokens` and `output_tokens` are enforced budget charges, including
conservative estimates for unavailable consumption. New usage snapshots include
optional `accounting: {version: 1, providers: [...]}`. Each bounded provider group
contains a `model_key` hash of the configured provider/model, call/active/failed
counts, `reported_calls`, `partial_usage_calls`, `unknown_usage_calls`,
`cache_known_calls`, charged input/output, reported input/output, and
`cached_input_tokens`. Cached tokens are included in reported input. Partial
measurements are lower bounds; estimates equal charged minus reported tokens.
Complete usage from a failed native response is retained. Missing usage does not
imply failure, and missing cache details do not imply zero cached tokens. The
projection is absent for historical records whose measurements cannot be
reconstructed. Counts use exact integers; no endpoint, credential or provider
diagnostic is part of the projection. Remote-reported delegation consumption is
separate from this shared local provider budget.
Checked workflows also expose `change_session_id` and `workflow_id` for exact
association with the proposal; legacy or unrelated invocations may omit them.

`GET /agent-runs/{runID}` returns the selected `run`, up to 256 related
`invocations` (`has_more` identifies truncation), and the last 256 retained
source-free `events` (`events_truncated` identifies potentially missing history).
The passive detail also includes `trace` and `cost`. `trace` version 1 correlates
the same shared budget across retries and children using an opaque `trace_id`,
a content `snapshot` hash and up to 256 `spans`. Span `id`/`parent_id` are 16-digit
hex identities; `run_key` and `operation_key` are SHA-256 hashes. Kinds are
`invocation`, `node`, `tool`, `provider`, `retry`, `summary`, `delegation` and
`check`. Status describes execution (`running`, `completed`, `failed`, `canceled`,
`timed_out`, projected `interrupted`, or `unfinished` when an enclosing attempt
has no registered invocation to establish current ownership), not the validity
of findings or checks.
`started` and optional `ended` are timestamps; `elapsed_nanos` is the measured
monotonic duration only when an end was recorded. `legacy`, `incomplete`,
`truncated` and `omitted` preserve unavailable history. The journal keeps at most
4096 spans, never evicting unfinished spans to admit another. Trace retention
does not remove cumulative budget accounting. Reading does not export telemetry.

`cost.status` is `estimated` only when a configured versioned table covers all
measured local token consumption; then `total` is present. Otherwise status is
`unknown`, `total` is absent, and `known_subtotal` prices only known consumption.
`price_version`, `currency` and fixed `reasons` identify the basis: missing table,
legacy usage, missing model rate, incomplete usage or unknown cache split.
These are token estimates, excluding remote agent consumption, other fees and
taxes. No model name, prompt, diagnostic or tool payload is exposed by the spans
or cost projection. Rates are configured locally; see `CONFIG.md`.

`trace.export` is a passive preview: `enabled`, configured `destination`, its
`fingerprint`, `finished` and `unfinished` retained span counts, and an optional
fixed `reason` (`not_configured` or `no_finished_spans`). Legacy or unfinished-only
history cannot be exported. Destination/credential-reference changes invalidate
the destination fingerprint independently of `trace.snapshot`; the credential
value and environment variable name are never exposed by the preview.

`POST /agent-runs/{runID}/export` requires strict JSON containing `project_id`,
`project_revision`, `snapshot`, `destination_fingerprint` and `confirm: true`.
The service rechecks checkout identity, retained content and destination before
one send. Unconfirmed/invalid requests return 400, a missing run returns 404,
and changed scope/snapshot/destination returns 409 without sending. As with
other run routes, `worktree_path` selects its guarded checkout service.

The response is a source-free receipt with the project identity, `run_id`,
`snapshot`, `destination_fingerprint`, `state`, `sent`, optional `accepted` and
`rejected` counts, and a fixed optional `reason`. `sent` is the number of spans
in the attempted payload, not proof of delivery. A valid acknowledgement yields
`exported`, `partial`, or `warning`; `unknown` omits acceptance/rejection counts
because the collector may already have received the data. HTTP, cancellation,
timeout, oversized or malformed replies never become a successful receipt.
No automatic retry occurs. Reading history or a preview never exports.

Export follows the [OTLP HTTP/JSON envelope](https://opentelemetry.io/docs/specs/otlp/)
with hexadecimal trace/span IDs, numeric enums, decimal-string timestamps and
only host-projected attributes: fixed service/scope, snapshot hash, history
flags, omitted/unfinished span counts, outcome, monotonic duration and optional hashed run/operation
identities. It excludes source, prompts, tool values, raw errors, model names,
resource detection and ambient `OTEL_*` settings. Only finished spans with real
end times are included; incomplete/truncated history remains labeled. Request
body, response headers/body and duration are bounded as described in `CONFIG.md`.

An existing domain checkpoint is returned as `input`; superseded or removed
proposals have no input control. No raw SDK session, prompt or model response is
returned by default. Explicit `include_text=true` returns `text` with availability,
`partial_model_output` or `final_model_output` provenance, at most 64 KiB of UTF-8,
and `truncated`. Final output references its immutable completion artifact.
Partial text is transient (at most 64 invocation buffers) and may be unavailable
after eviction or restart. Text has no approval or execution authority.

The same explicit disclosure includes up to 16 `tool_records` from the selected
invocation's last 128 private events. Each record identifies its event, call,
tool name and kind (`arguments` or `result`); `value` is JSON text bounded to
2,048 UTF-8 bytes, with `truncated` identifying omitted text or nested values.
Names and call IDs are bounded to 128 bytes, and nesting to 24 levels.
`tools_truncated` identifies omitted history. Credential-bearing fields such as
authorization, API keys, passwords, tokens and private keys are redacted;
arbitrary prose may still contain private source and must remain an explicit,
local disclosure. Tool records are absent from default details and progress SSE.

`GET /agent-runs/events` returns a JSON replay batch, or SSE when Accept contains
`text/event-stream`. SSE frames use `event: agent_progress`, `id: <cursor>` and
the same JSON batch in `data`. The cursor has an opaque journal epoch and a
monotonic sequence. Pass it as `cursor` or `Last-Event-ID`; conflicting values
are rejected. Batches contain at most 128 events; `has_more` requests immediate
replay. With no cursor, a foreign/future cursor, or a cursor older than the 2,048
retained events, `reset=true` returns a current root snapshot and its new cursor,
without replaying older transitions over that snapshot. Paginate the snapshot's
`next_cursor` if needed. Subsequent batches contain only newer events. Consumers
deduplicate by epoch/sequence and replace state on reset. Detail views expose
retained earlier node transitions separately.

Events carry root `run_id`, native `invocation_id`, parent/node identity, project
revision, time, state, shared usage, input request identity and immutable evidence
references. They contain no source, prompts, arbitrary provider metadata or model
text. Durable terminal transitions survive missed broker notifications. Frames
are capped at 1 MiB, slow writes at ten seconds, and idle streams refresh every
25 seconds to recover dropped notifications. Disconnect/cancel releases the
subscription; reconnect rechecks project scope. Existing `/events` ready/update
subscribers retain their original contract.

`POST /agent-runs/{runID}/control` requires `project_id`, `project_revision` and
`action: cancel`. Only a current active root with `can_cancel=true` is eligible;
the shared budget is canceled and the worker owns cleanup. History cannot create
or retry arbitrary graphs. Waiting input is resolved through
`POST /agent-runs/{runID}/input` with the same `ChangeIdentity`, `workflow_id`,
`request_id` and `decision` as the workflow input endpoint. It routes to the
proposal owner, retaining all fresh source, check, scope and explicit Apply guards.
Missing invocation IDs return 404, stale identities 409, and unreadable journals
500; none is interpreted as empty successful history.

Workflow progress and advisory review are private source-bearing conversation
history. List entries add `workflow_status` without review contents. Schema-1
history without the optional `workflow` field remains readable. After a daemon
restart, unfinished workers read as interrupted and do not automatically dispatch.
Waiting input stays visible without resuming its graph. Resume clears check/review
authority and marks workflow evidence outdated. An unchanged approval checkpoint
can be answered after explicit fresh checks; a new workflow requires fresh
admission. A saved approval receipt never restores in-memory Apply authority.
Progress persistence failures remain
visible and cannot authorize Apply. No provider or project-code execution occurs
when reading progress, restoring history or configuring the workflow.

Grouped writes are journaled before source replacement. A failed write rolls back
when current hashes still match; an unrelated concurrent edit blocks recovery
instead of being overwritten. `prepared`, `undoing` and `recovery_required` receipts
expose interrupted/partial work and must be recovered before another grouped Apply.
A mutation receipt reports the actual state, index (when available), warnings and
Undo availability. Metadata failure after source changes does not erase the receipt.
Undo verifies post-Apply hashes and removes files that the proposal created. The
latest grouped change owns the Undo journal.

`POST /api/projects/current/changes/{sessionID}/verify` accepts `ChangeIdentity`:
current `project_id`/`project_revision` plus applied proposal `revision`/`hash`.
It requires the latest journal to be applied and every post-Apply target and
workspace hash to match. Go verification requires fresh execution trust, then
checks source parse/format and runs fixed `go test ./...` and `go vet ./...` in
an isolated copy. Other code runs the project's declared task-runner test and
check targets; without a runner the status is `unavailable` because nothing
executed. Documentation-only verification checks text/file identities without
executing code. It sends no model request and never changes source. Results carry
`session_id`, project identity, `proposal_hash`, `workspace_hash`, `status`, optional
`reason`, checks and timestamp. Status distinguishes `verified`, `failed`,
`unavailable` and `canceled`; later source/project changes expose evidence as `stale`.
Evidence is private journal metadata and appears in passive recovery receipts as
`verification`. Undo clears it. Failed checks leave applied source in place.
Verification proves only those checks; it does not certify all acceptance criteria
or automatically resolve findings. Explicit focused reanalysis uses the existing
file-analysis endpoint and its own Bug-scope provider consent.

Features use the configured Analyze scope. Generation/goals bodies carry current
project identity, `expected_hash`, `goals` (at most 4096 bytes), and generation's
`confirm_remote_provider`. Triage adds `status` (`open`, `saved`, `dismissed`).
Each generation requests at most five ideas; each includes benefit, evidence,
eligible affected paths, estimated effort and acceptance criteria. Ideas remain
advisory and never enter Bugs/Performance/Security finding counts. Standalone
generation via `POST /api/projects/current/features/generate` accepts an
optional `profile` (catalog model ID or legacy `analyze`, `bug`, `function`;
default `analyze`) to choose the model, and an optional `analysis_selection_id`. When the selection ID
is present, its fingerprint is validated (409 on mismatch) and its exclusions
are applied to the context and suggested paths.

Standalone generation is synchronous and uses the same workflow deadline as
analysis feature discovery: at least ten minutes, or the selected profile's
timeout when longer. The native client waits for the workflow result or caller
cancellation. The daemon suspends its ordinary response write deadline during
generation and restores the bounded write allowance before sending the result.

Every generation, from the Features routes or an analysis run, is additive. The
prompt receives a bounded list of existing idea titles and statuses, newest first
(at most 8 KiB of whole lines, with a count of omitted older titles), and the
token-budget check counts it with the context and goals. After the expected-hash,
project and source checks, the daemon appends only new ideas with status `open`.
A candidate is a duplicate when its `id` matches an existing idea, or its
normalized title matches any existing idea (dismissed included) or an earlier
candidate in the same response. Normalization lower-cases Unicode text, turns
each run of non-letter, non-digit characters into one space and trims; empty
keys never match. Existing ideas keep their order, IDs and statuses, so a
duplicate never reopens a dismissed idea. An empty or duplicate-only response is
a successful generation with no new ideas.

Feature history is `schema_version: 2`. `generations[]` records each successful
generation's `id`, `generated_at`, `project_revision`, `workspace_hash`,
`goals_hash` and `model` (`profile`, `model`, `provider_origin`,
`remote_provider`, or null when unknown); each idea has `generation_id`.
`last_generation` reports `generation_id`, `added_count` and `duplicate_count`
for the most recent successful generation. Records no longer referenced are
pruned on write, and unresolved or duplicate references are rejected as
unsupported history. Each idea's `freshness` is derived on read: it is `current`
only when its own generation's revision, workspace fingerprint and goals hash
match the current values. The report-level `freshness`, `project_revision`,
`workspace_hash` and `context_manifest` describe only the most recent generation,
so a later generation never revalidates older ideas. Version 1 history remains
readable: its ideas share a `legacy` generation built from the v1 report context,
with goals trusted only if v1 marked them current and a null model. The stored
`hash` is kept until the next successful write saves version 2.

The history stays bounded at 64 KiB. A generation that would exceed it adds no
ideas, leaves the file unchanged and fails with `feature history is full;
existing ideas were kept`; a v1 upgrade that would exceed it fails the same way
with the v1 file intact. Failed generation retains earlier ideas with an
explicit failure status. Its `failure` explains whether the provider request,
response format, or suggestion validation failed without storing raw provider
diagnostics or rejected output. If that status cannot fit, the generation error is
returned and the file is unchanged. Cancellation, project/source/goal changes,
a concurrent report change and persistence errors write nothing new.

The web Summary reads saved feature suggestions without generating them. Its
Features count includes open and saved ideas and links to the Features workspace.
Unavailable or ungenerated counts remain unknown; a successful empty report
shows zero. Stale and failed reports retain their status indicators and any
available saved counts. Features keeps the full ideas, estimated effort, error
details and passive handoff to Chat.

## Git and worktrees

Repository/workspace reads require current `project_id` and `project_revision`.
`available: false` represents a non-Git directory; a detached branch or unborn
HEAD stays distinguishable from read failures. Worktree status failures have an
`error` and omit `dirty`; unknown status is never treated as clean. Lists are
bounded to 100 worktrees. Completed entries are applied, committed change history.
Each worktree has `mergeable`, computed against the current branch: both checkouts
must be clean local branches without an operation in progress, the source must be
unlocked and present, and it must have commits not already in the target with a
shared ancestor. Saved tasks must all be applied and committed; unfinished or
failed tasks block merge. This passive eligibility check does not predict conflicts.
The full list remains available even when no worktree is eligible.
Passive status/diff reads disable configured clean/process filters, external diff
commands and text conversion; they do not grant project execution trust.

Git mutations carry that project identity and the displayed `head`/`branch`,
`confirm: true` and `confirm_execution: true`. They also require existing execution
trust for the current project revision, since checkout filters and merge drivers
can execute project code. Hooks, signing, editors and credential prompts are
suppressed. Switch/merge/abort cancel prepared jobs, invalidate review/execution
trust and reindex even after a Git command fails. A command failure can leave
source changes; its receipt preserves `error`, observed repository status and
index/warnings. Transport failures require status/recovery refresh before retry.

Creation adds `target_branch` and an unused absolute `path` directly inside the
current project's `.worktrees/` directory. The daemon creates that directory,
rejects symlinked parents, and adds `/.worktrees/` to Git's local `info/exclude`.
Project indexing, context policy and isolated checks also exclude `.worktrees`;
creating or editing an implementation does not change the original source revision.
It runs `git worktree add` from the captured HEAD; uncommitted content is not copied.
A local inventory marked AI-unavailable allows the new checkout to reopen without
model access. Creation returns its path without changing the active project.

A task can get its own worktree: `POST /api/projects/current/changes` with
`worktree: true` and `confirm_execution: true` (and the usual task fields and
this checkout's identity) creates branch `orca/<title slug>-<6 hex>` (the
slug is the title's first words, within 24 characters) and
`.worktrees/<same name>` from the current HEAD under the creation guards above,
then captures the task there with the worktree's own project identity. It needs
a Git repository on a branch with a commit, and the task's paths must have no
uncommitted or untracked changes here, since the worktree would not have them
(400 names the first such path). The worktree is trusted to run checks when this
checkout is trusted and the worktree's command scope is identical, and it gets
the agent settings saved here when they were saved for the same configuration;
otherwise `warnings` says what it lacks. The response is the task plus
`worktree_path`, `branch` and `warnings`; later requests for the task carry
`worktree_path`. If the task cannot be captured, or the checkout fails after Git
made the branch, the worktree and branch are removed again, even when the
request was canceled (or the error names what was kept).

Project endpoints accept an optional `worktree_path` query parameter, an absolute
path to another registered worktree of the active repository. Membership is
rechecked on each request; unrelated, missing and pruned paths are rejected.
`GET /api/projects/current?worktree_path=...` returns that checkout's project identity.
Subsequent requests use its own project ID/revision and ordinary proposal guards.
Each checkout has a separate manager, execution trust, proposal authority and
bounded workflow worker. Reads and navigation never start generation or transfer
consent. The client keeps the original branch selected while
separate implementations run; task Review/Apply/Commit/Undo target only their
captured checkout. Daemon shutdown and explicit project replacement stop these
workers; reindexing or switching branches in the original checkout does not
replace their task state. Existing initialized worktrees outside `.worktrees` remain readable.

Branch switching takes an existing `target_branch`; dirty worktrees and branches
checked out elsewhere block the switch. To return to a branch already checked
out elsewhere, explicitly restore that existing project path instead of switching
the current worktree to the same branch.

Merge preview and merge take the selected worktree `path` and `source_head`.
Preview returns source/target identities, incoming commits and a bounded read-only
diff; it requires no execution trust and never merges. Merge
rechecks both heads (and `source_branch` when supplied) and requires both worktrees
clean and all saved tasks applied and committed. Merge conflicts return
`state: conflicts`, `repository.merge_head` and `repository.conflicts` instead of
success. Resolve them using ordinary Git tools, or explicitly abort by sending
the displayed `merge_head`; abort discards conflict-resolution edits.

After a successful merge, the daemon marks findings linked to applied, committed
fix sessions Done in the original project. It captures the links before merging
and verifies each fix commit belongs to the captured source HEAD. Drafts, undone
fixes, failed commits, commits outside that history and other projects' targets
do not complete findings. Completion is persisted before worktree removal and
survives reindexing, stale analysis and restarts. `GET /api/projects/current/findings`
returns these references with their `commit_oid` in `completed`, independently of
finding filters. Semantic findings also use the existing `status: fixed`; explicit
reopen or dismissal clears their completion. Confidence, verification and freshness
are unchanged. The client labels completed findings **Done** in lists and details.
If saving completion fails, `state` remains `merged`, `warnings` explains the
failure, and the worktree and its task history are retained. Retrying the guarded
merge after fixing storage saves completion even if Git has already merged the HEAD.

With `keep_worktree: true` the merge stops here: findings are completed, and
the worktree and its branch stay (`removed_worktree` and `removed_branch` are
omitted) until `cleanup-merged` removes them. Otherwise the daemon then stops
the source implementation worker,
rechecks the source identity and clean state, verifies its commit is in the current
branch, and removes the source checkout with `git worktree remove` without force.
After checkout removal, the source branch is deleted only if its captured commit
is integrated and no registered worktree still uses that branch. Ref deletion
compares the captured HEAD atomically, preserving concurrently advanced branches.
Private `.mini-orca/` task history is locally ignored for removal and deleted
with the checkout. Checkouts containing another
registered worktree are retained. Conflicted, failed and aborted merges never
complete findings or remove the source. Successful removal returns `removed_worktree`
with its absolute path, allowing the client to close the implementation and stop polling it. If
cleanup fails, the merge remains `state: merged` and `warnings` explains why the
worktree remains; `removed_worktree` is omitted. `removed_branch` independently
confirms branch deletion. If only branch deletion fails, the worktree removal
receipt is retained and a warning identifies the branch to inspect in Git.
`completed` contains the finding completions saved by this operation, including
when subsequent cleanup fails; clients must not infer completion from a worktree's
applied history alone. No push happens automatically.

`cleanup-merged` provides the same completion and removal path after an external
merge commit or an earlier cleanup warning, without running `git merge` again.
It requires the current project/revision, destination HEAD/branch, source path,
source HEAD/branch, both confirmations and execution trust. The destination must
be clean and have no active Git operation; the source must be an ancestor of its
HEAD. Unfinished task histories, changed, dirty, locked or nested source worktrees
remain protected. Successful reconciliation also refreshes project facts after
the external merge. After external source changes, explicitly reindex and grant
execution trust for the updated revision before requesting cleanup. Squash
merges and cherry-picks without ancestry are not treated as proof of integration.

Workspace reads add `integration` (`current`, `merged`, `up_to_date`, `behind`,
`unrelated`) and `overlap_paths` for paths changed on both sides of a divergent
branch. These are passive comparisons, without merging or running merge drivers.
Overlap is advisory, not proof of conflict. `merged` includes equal HEADs; it does
not claim a task was verified. The Web UI gives unfinished task states precedence
over ancestry, so a failed task at the destination's HEAD is shown as `FAILED`.
Applied, committed work awaiting explicit integration is shown as `TO_MERGE` and
its linked issues are withheld from the issue lists until merged or rejected.
Unknown integration is omitted with `error`.
Refreshing never removes a worktree or completes findings.

Discard takes the selected worktree `path`, `source_head` and `source_branch` plus
the usual mutation identity, confirmation and execution trust. It rechecks the
clean source and branch, stops its worker, and removes the checkout and private
task history without merging or forcing deletion. The current checkout remains
unchanged. The source branch is kept when it has commits the current branch
lacks; otherwise nothing would be lost, so it is deleted with the same guards as
after a merge and returned as `removed_branch` (a failure is a warning). Locked, stale, dirty and nested checkouts are protected
by the same removal guards as merge cleanup. Success returns `state: discarded`
and `removed_worktree`; removal failure returns `state: failed` with `error`.
Discard reopens linked in-progress findings, including applied but unmerged fixes,
unless another task still owns the finding. Any status persistence failure is
reported in `warnings` without hiding the successful removal receipt.

### Project setup

Every setup request carries the active project's `project_id` and
`project_revision` (query parameters for GET, body fields for POST); a stale
pair returns 409. GET returns `state`, `config_file` (the project's
`.mini-orca/config.yaml`, existing or not), `daemon_config_file` (empty for
the built-in defaults), `error` for an `invalid` file, `models` keyed `analyze`,
`bug` and `code` (`provider`, `model`, `api_base_url`, `vertex_project`,
`vertex_location`, `cli_path`, `reasoning_effort`, optional `temperature`,
`max_tokens`, `context_max_tokens`, and `api_key_set` — a key is never
returned), `roles` (`creation`, `testing`, `review`, each a scope) and
`limits` (the seven budget values) and `timeouts` (`import_seconds`,
`analysis_seconds`, `generation_seconds`, `workflow_model_seconds`,
`focused_check_seconds`, the values in effect with defaults filled in). POST
takes `models`, `roles`, `limits` and optional `timeouts` in the same shape
(omitted, the time limits are kept; given, each must be 1–86400 seconds); an empty `api_key` keeps the configured key for the same
provider and `clear_api_key: true` removes it. The result is validated on top
of the daemon's configuration (400 with the field and reason), written `0600`
with the file listed in `.mini-orca/.gitignore`, and applied at once; other keys
of an existing file and their comments are kept. A running analysis, agent
run, Go scan or worktree work returns 409 naming it. Saving resets the agent
overrides of `…/agent-settings` and then reports `overrides_reset: true`. A `configuration` event
announces every applied change. `…/setup/skip` records the choice in
`.mini-orca/setup.json`. `…/setup/timeouts` takes `timeouts` alone, writes
only that section with the same checks, busy refusal and live apply, keeps
agent overrides, and returns the setup. `…/setup/test-model` takes `scope` and `model` and
returns `ok`, `model`, `latency_ms`, and for a failure `reason` (the same kinds
as analysis stages) and `detail`; an invalid model configuration is a 400. The
test is admitted under the configuration's own agent settings, so out-of-date
overrides never block it.

Opening or switching to a project loads its configuration file. An invalid
file leaves the daemon's configuration in use and is reported by setup.

### Agent settings and skill validation

GET `/api/projects/current/agent-settings` requires current `project_id` and
`project_revision`. Its no-store response contains the configuration fingerprint,
settings `version` (zero before the first edit), `state` (`configured`, `overridden`
or `stale`), effective `roles`, `limits`, operator-configured `ceilings`, configured
skill directories with `enabled` flags, non-secret configured model metadata, and
`role_models`, the model each role runs with.
This local read creates no journal, connection, process or provider request.

POST `/api/projects/current/agent-settings/control` requires the same project
identity, `configuration`, `version`, `confirm: true` and `action: replace` or
`reset`. Replacement requires complete `values`: `roles` with `creation`, `testing`
and `review`, each a configured scope (`analyze`, `bug` or `code`) or a model ID from
`GET /api/models/available` (configured, Agy or Pi models; the catalog is checked
when saving and again when a run starts); all seven `limits`
fields; and an explicit `disabled_skills` array drawn only from configured skill
directories. Positive limits must remain within the configured ceilings; `repairs`
may be zero. No provider, credential, tool, skill directory or retention permission
can be added. Reset omits `values` and follows current operator defaults while
advancing the version, so old editors cannot overwrite a reset.

Settings are private to the canonical checkout and survive restart. Mutation
requires current project/configuration/version identity. Operator configuration
changes, including removing a configured model a role selected or any change
of the models (a project configuration file included), mark retained overrides
stale; new work, analysis included, then answers 409 with a message that
names Settings → Models & agents and requires an explicit reset,
while retained data remains readable. Malformed requests return 400, stale scope
or version returns 409, and corrupt or failed persistence returns 500 without
replacing retained data. Existing runs keep their admitted roles, budget, skill
selection, context policy and configuration fingerprint. Explicit per-request
model choices still take precedence. A pending pure human-review handoff keeps
its captured settings but rechecks operator, project, policy and proposal identity.

POST `/api/projects/current/agent-settings/skills/validate` requires current
project/configuration/version identity and a configured `directory`. It reads the
indexed manifest and resources through current source/policy guards, uses the
native ADK parser, and returns `state: valid` with bounded metadata, required tool
names and resource hashes/sizes. It returns neither instructions nor resource
bodies, writes no validation grant and executes no scripts. Validation does not
enable a skill or grant its required tools; actual investigations revalidate
source and check tool availability in their own admitted scope. Changed, excluded,
malformed, oversized or unindexed resources remain explicit errors.
