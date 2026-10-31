# Configuration Guide

Mini-Orca reads YAML configuration at two levels: the daemon's `config.yaml`
(see [Configuration changes](#configuration-changes) for where it is found)
and, for each project, that project's own `.mini-orca/config.yaml`, which the
first-run setup in the Web UI writes (see [Project configuration](#project-configuration)).
Start from `config.example.yaml`; do not commit provider credentials. Unknown or
retired keys are refused with the full field path in the error. JSON
configuration files are not supported.

The daemon configures three model profiles, used by default as follows:

| Scope | Used for |
| --- | --- |
| `analyze` | Project import, architectural summaries and source-based Performance review |
| `bug` | Selected-file and unified semantic analysis |
| `code` | File proposals and explicit repairs |

The Chat agent workflow chooses a model independently for Creation, Testing
and Review. Their prompt scopes are `bug`, `code` and `analyze`, respectively. Choose
configured profiles or discovered Agy/Pi catalog models on **Models**. A captured run
keeps its selected model, timeout and retry budget; remote consent covers every
selected remote profile. These selections do not change other operations' defaults.

All three scopes are required. Each selects a `provider` (`openai`, `gemini`,
`vertex`, `agy`, or `pi`) and a `model`. Omitted `provider` retains the OpenAI-compatible HTTP
transport. HTTP profiles have a Chat Completions-compatible
`api_base_url`; an empty `api_key` is valid for a loopback local
server. `api_base_url` must be an absolute HTTP or HTTPS URL without user
information, a query string, or a fragment. A non-loopback scope still requires
an explicit confirmation on each request that sends prompt content. Configuration
does not authorize automatic scans, writes, multi-file edits, commits, or pushes.

```yaml
model_scopes:
  analyze:
    api_base_url: "https://api.openai.com/v1"
    api_key: "set-in-ignored-config.yaml"
    model: "analysis-model-id"
    reasoning_effort: "high" # Optional: none, minimal, low, medium, high, xhigh, max.
    temperature: 0.1 # Optional; defaults to 0.7.
    max_tokens: 16000 # Optional; defaults to 8192.
    context_max_tokens: 120000 # Optional; defaults by scope.
  bug:
    api_base_url: "http://localhost:11434/v1"
    api_key: ""
    model: "bug-model-id"
  code:
    api_base_url: "http://localhost:1234/v1"
    api_key: ""
    model: "code-model-id"
```

The default context budgets are 120000 tokens for `analyze`, 32000 for `bug`,
and 4000 for `code`. For HTTP profiles, `temperature` must be from 0 through 2;
`max_tokens` must be 1–65536; `context_max_tokens` must be 1–120000. API keys
are never returned, logged, or embedded in metadata. Mini-Orca does not perform
environment interpolation or use a vault or Keychain.

The ADK runtime supports bounded Chat Completions, typed function calls and text
streaming through the OpenAI-compatible adapter, plus native Gemini through the
pinned Google SDK. Existing proposal workflows publish only complete results.
Native Anthropic Messages, OpenAI Responses and HTTP model enumeration are not
implemented. Provider errors are sanitized; raw provider bodies are never diagnostics.

## Native Google providers

Replace a scope with `provider: gemini` for the Gemini Developer API:

```yaml
analyze:
  provider: gemini
  model: exact-google-model-id
  api_key: set-in-ignored-config.yaml
  max_tokens: 16000
  context_max_tokens: 120000
  reasoning_effort: high
```

The default endpoint is `https://generativelanguage.googleapis.com` using
`v1beta`. Use `provider: vertex` with an explicit `api_key` for Vertex Express,
or use a project-bound access token:

```yaml
analyze:
  provider: vertex
  model: exact-google-model-id
  access_token: set-in-ignored-config.yaml
  vertex_project: your-project-id
  vertex_location: global
```

Vertex uses `v1` at `https://aiplatform.googleapis.com` for Express/global, or
`https://LOCATION-aiplatform.googleapis.com` for a configured region. Tokens
must be refreshed in the local configuration and the daemon restarted when they
expire. Do not combine Express keys with a token or project/location. Native
profiles never authenticate with environment credentials or Application Default
Credentials. They cannot reuse an Antigravity account implicitly.

An optional `api_base_url` selects an explicit gateway root; the adapter appends
the API version and model path. Native endpoints require
HTTPS, except loopback HTTP for local gateways. Credentials are attached only to
that endpoint. Redirects, SDK retries, and SDK continuation requests are blocked;
each model attempt permits one HTTP exchange and retains the application's deadline.
Requests and responses are bounded to 4 MiB. Incomplete, blocked, malformed, or
tool-bearing completion results cannot become proposals.

Native profiles support `temperature`, `top_p`, `top_k`, `presence_penalty`,
`max_tokens`, and the existing context budget. `reasoning_effort` accepts `none`
(zero thinking budget), `minimal`, `low`, `medium`, or `high`; support depends on
the selected model. `min_p`, `repeat_penalty`, `xhigh`, and `max` are rejected.
Policy messages must precede the conversation for native Google requests.

Model metadata includes adapter capabilities for completion, JSON Schema,
typed tools, and streaming. This describes implemented protocols, not account
entitlements or model availability. Reading
configured metadata is passive; only explicit CLI catalog refresh performs
discovery. Native models are selected by their configured identifier.

The native provider paths are covered with fake HTTP providers, including Vertex
authentication, cancellation and tool-signature round trips. No live Google
account or model has been used for this validation. See the
[Google ADK model documentation](https://adk.dev/agents/models/google-gemini/).

## CLI providers

Install and sign in to [Antigravity CLI](https://www.antigravity.google/docs/cli/install/),
[Pi](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/README.md)
or [Claude Code](https://code.claude.com/docs/en/setup)
as the user running the daemon. Choose a model supported by that CLI (`agy models`,
`pi --list-models`, or a Claude Code alias such as `sonnet`, `opus`, `haiku` or
`fable`, or a full Claude model name). The daemon invokes the executable directly, without a
login shell. Make it available on the daemon's `PATH`, or set `cli_path` to an
absolute executable path. It is not a shell command and accepts no extra arguments.

For example, replace the three scope blocks with:

```yaml
model_scopes:
  analyze:
    provider: agy
    model: gemini-3.8-flash-high
    reasoning_effort: high
  bug:
    provider: pi
    model: provider/exact-model-id
    context_max_tokens: 32000
  code:
    provider: pi
    model: provider/exact-model-id
    reasoning_effort: high
    # cli_path: /absolute/path/to/pi
```

Or run each stage on a different subscription, with no API key anywhere:

```yaml
model_scopes:
  bug:          # Creation: Claude Code on your Claude plan
    provider: claude
    model: sonnet
  code:         # Testing: agy on your Google AI plan
    provider: agy
    model: gemini-3.8-pro-high
  analyze:      # Review: pi on the ChatGPT plan it signs in with
    provider: pi
    model: openai-codex/gpt-5.5
```

CLI and HTTP scopes can be mixed. CLI profiles use the CLI's own credentials
and settings; omit `api_base_url`, `api_key`, `temperature`, `max_tokens`, and
sampling options. Unsupported settings fail startup instead of being ignored.
Context budgets still limit Mini-Orca's supplied context. Pi maps reasoning
`none` to `off`; agy and Claude Code support `low`, `medium`, `high`, `xhigh`,
and `max` (Claude Code's `--effort`; available levels depend on the model).
Use exact model identifiers, not Pi fuzzy patterns or thinking suffixes.
For agy, copy the slug from `agy models`: for example, `gemini-3.8-flash-high`
selects Gemini 3.8 Flash (High). Display names such as `Gemini 3.8 Flash` are
invalid; names containing spaces fail configuration validation with instructions
to use a slug. Model availability depends on the CLI account. Restart the daemon
after changing its configuration.

Analysis and workflow stages can also select models from the live Agy catalog or Pi's available-model
catalog without adding them to a scope in `config.yaml`. The picker reads
`agy models` for exact slugs and display names, including model variants, and
Pi's `get_available_models` RPC catalog, including custom local models from Pi's
`models.json`. For each CLI it uses the first matching configured `cli_path`
(Analyze, Bug, then Code), or `agy`/`pi`/`claude` on the daemon's `PATH` if no scope
uses that CLI. No prompt or project context is sent during discovery. Agy may
contact its catalog service; Pi discovery uses its offline inventory. Claude Code
has no catalog command: when its executable is installed, the picker offers its
aliases (`opus`, `sonnet`, `haiku`, `fable`), which follow the newest model of
each family; whether your plan includes a model shows when a run starts.

Use **Models → choose a model → Agy** to browse Agy directly. **Refresh models**
reloads both catalogs after account or model changes. Each CLI's discovery
failure is reported separately and leaves configured choices and the other
CLI's catalog available. Local catalog entries still require prompt consent
because the CLI controls its downstream destination. Catalog assignments use
the operation's context/dispatch limits and CLI defaults, with no inherited
reasoning override from a configured scope. Agy variants retain their exact slug.

For Gemini through an Antigravity account, use Agy directly. Pi removed built-in
Google Antigravity and Gemini CLI support in
[0.71.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md#0710---2026-04-30).
Pi still supports the Google Gemini API with its own authentication, such as
`GEMINI_API_KEY`; signing in to Agy does not populate that Pi provider. See
[Pi providers](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/providers.md).
Mini-Orca disables Pi extensions for both discovery and inference, so providers
added only by a Pi extension are not included.

Each request starts a separate process in a private temporary directory, using
Mini-Orca's supplied source context. Pi runs in JSON print mode with tools,
extensions, MCP, skills, context-file discovery and session saving disabled. Its
temporary project settings disable automatic retries and compaction. Agy uses
a temporary `mini-orca` agent with file, command, subagent and MCP tools disabled,
slash expansion disabled, and JSON stdin/stdout. Claude Code runs in print mode
(`-p --output-format stream-json`) with its built-in tools off (`--tools ""`),
no MCP server (`--strict-mcp-config`), `dontAsk` permissions, only the empty
directory's project settings (so no user hooks, plugins, skills, memory files or
`apiKeyHelper`), slash commands and skills disabled, no saved session, Mini-Orca's
system prompt, and one turn. Agy also keeps `finish` disabled:
native schema completion depends on that tool, which user hooks can reject.
These adapters require CLI versions supporting those options and the documented
event formats.
See the [Pi CLI reference](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md)
and [agy headless reference](https://www.antigravity.google/docs/cli/headless/).

Only a successful final assistant response is accepted. Both CLI providers
receive the JSON Schema in their system prompt and must return a JSON answer.
Mini-Orca validates the final response against that schema locally, with no
unconstrained fallback. Failed, truncated, tool-bearing or incomplete responses
fail the request.
CLI stderr and raw protocol errors are not returned or logged. Output is bounded
to 4 MiB and stderr to 64 KiB. HTTP and CLI transports honor the workflow's
bounded deadline. Direct
transport callers without a deadline receive a five-minute fallback. Owned process
groups are stopped on completion or cancellation, and temporary prompt files are removed. CLI providers currently
require Unix process-group support; HTTP providers retain their existing platforms.

CLI providers always require remote-provider confirmation, even if Pi is
configured with a local model: Mini-Orca cannot independently verify the CLI's
downstream destination. Metadata shows `cli://agy`, `cli://pi` or `cli://claude`, with
`temperature` and `max_tokens` set to `null` because those controls are unavailable.
Existing Review, explicit Apply, and Undo still own source changes. Mini-Orca
does not configure the CLI's credential storage or upstream retention; agy may
retain its own conversation records under its normal settings. Application
retry limits count CLI invocations; upstream retries internal to a CLI or its
service are outside Mini-Orca's transport accounting.

### Subscriptions, not API keys

The CLIs run on the subscription their user signed in with: Claude Code on the
Claude plan (`/login` with a Pro or Max account), agy on the Google AI plan, pi
on the ChatGPT plan (or a local model). Mini-Orca never reads or copies a CLI's
login. Claude Code would bill an API key or a cloud account found in its
environment, so Mini-Orca starts it without any `ANTHROPIC_*`,
`CLAUDE_CODE_USE_*` or Bedrock token variable, loads no user settings (where an
`apiKeyHelper` could live), and stops a run before the model is called when
Claude Code still reports an API-key source; sign out of the key and sign in with
`/login`.

When a CLI reports that its plan reached its usage limit (Claude Code's
rejected rate limit or "limit reached" result, agy's exhausted quota, pi's usage
limit), the request is not retried. A workflow **pauses** at that stage: its
status is `paused`, with the stage, the CLI, the reset time when the CLI gave
one, and the start that continues it. Nothing the stage left half done is kept.
Resume the run when the limit resets, or choose another model for that stage.

### Tool-using agents

A task in its own worktree whose creation or testing model runs in pi, Claude
Code or agy gets a tool-using agent for that stage. Its only tools are those of a
per-run MCP server on a loopback port with a one-time token; a call to any other
tool stops the run.

- **pi** loads only Mini-Orca's extension, which registers the server and blocks
  every other tool; the token reaches it through the environment.
- **Claude Code** gets the server in a private `--mcp-config` file
  (`--strict-mcp-config`, built-in tools off, `--allowedTools mcp__mini-orca`,
  `dontAsk` permissions, `--max-turns`). A run that cannot connect to the server
  stops before the model works.
- **agy** gets the server in the run's private workspace
  (`.agents/mcp_config.json`, limited to the run's tools) and a `mini-orca`
  agent with no built-in tools, no command execution and no subagents. agy's
  model calls MCP tools through one `call_mcp_tool` tool, so the agent's prompt
  lists each of Mini-Orca's tools with its arguments schema, and only a call
  naming the `mini-orca` server and one of the run's tools is accepted. Headless
  agy denies an MCP tool that would ask for approval, and Mini-Orca does not
  skip agy's permissions, so allow Mini-Orca's server once in
  `~/.gemini/antigravity-cli/settings.json`:

  ```json
  {"permissions": {"allow": ["mcp(mini-orca/*)"]}}
  ```

  A run whose tools agy denied fails with this instruction.

## Agent roles and run limits

`agent_runtime.roles` supplies Creation, Testing and Review profiles when an API
request omits a model choice. Defaults are `bug`, `code`, and `analyze`.
Explicit catalog selections take precedence. Resolved roles are frozen at
admission, saved with the workflow, and require the same profile-specific remote
consent as explicit choices. The runtime never silently switches providers.

```yaml
agent_runtime:
  roles:
    creation: bug
    testing: code
    review: analyze
  max_model_calls: 1024
  max_tool_calls: 4096
  max_input_tokens: 134217728
  max_output_tokens: 67108864
  max_elapsed_seconds: 86400
  max_concurrent_calls: 4
  max_repairs: 2
```

Model calls, tokens, concurrency and elapsed time are shared by child stages and
provider retries within an admitted workflow or analysis execution window.
Standalone operations receive their own budget. Existing scope deadlines and
analysis attempt allowances still apply; the first exhausted limit stops work.
Limits are positive; omitted or zero values use the displayed defaults, except
`max_repairs`, where an explicit zero disables repairs. Repairs remain opt-in and
cannot exceed two. Maximums are 4096 model calls, 16384 tool calls, 2^40 tokens per
token limit, seven days, and 32 concurrent model calls. Completion agents admit
no tools; their ADK policy hook rejects tool dispatch.

Before dispatch, the runtime reserves input capacity using encoded request bytes
plus framing allowance, and output capacity using the configured `max_tokens`
(4 MiB for an opaque CLI). Reported usage settles reservations; Gemini thinking
tokens count as output. Missing usage keeps the full reservation and remains
unknown. Valid complete receipts from failed native calls retain their reported
consumption; interrupted native streams retain partial measurements and charge
at least the reservation until full consumption is known. A provider usage overrun stops the run and
prevents final-result publication. These are local execution limits, not a billing
guarantee: upstream internal retries and unreported consumption remain unknown.
No reservation or usage record grants source-write or execution authority.

The Task Usage panel separates enforced budget charges, provider-reported input
and output, cached input, and conservative estimates. New records retain a
versioned accounting projection grouped by hashes of the configured provider and
model; endpoints, credentials and diagnostics are excluded. Cache counts are a
subset of input tokens, never an additional charge. Missing cache details remain
unknown. A successful response without usage is not labeled a failed call.
Historical records without this projection retain their original totals without
inventing measurements. Remote delegation usage remains separately labeled in
External Work and is not added to local provider measurements. Cost is unknown
without a versioned price table.

Local traces retain bounded invocation, node, provider, tool, retry, summary,
delegation and check spans. They contain opaque identities, hashed operation
names, fixed outcomes and timing; prompts, source, tool values and diagnostics
stay outside this projection. The private journal retains at most
4096 spans, evicting completed spans first and reporting omissions. Unfinished
spans have no measured end time. Traces are never sent off the machine;
ambient `OTEL_*` exporters and resource detection are not enabled.

`agent_runtime.telemetry.export` (manual OTLP trace export) has been removed;
because the configuration schema is strict, a configuration that still sets it
fails to load with an error naming that key.

Optional `agent_runtime.telemetry.prices` supplies a user-maintained token price
table. Set `version` to a nonempty identifier, `currency` to three uppercase
letters, and `rates` to 1–64 unique configured `provider`/`model` pairs. Every rate
requires explicit `input_per_million`, `output_per_million` and
`cached_per_million` values between 0 and 1,000,000; zero explicitly means free.
Rates are never fetched automatically. Update the version when changing them.
For example, this is a demonstration table, not current provider pricing:

```yaml
agent_runtime:
  telemetry:
    prices:
      version: example-v1
      currency: USD
      rates:
        - provider: openai
          model: your-configured-model
          input_per_million: 2
          output_per_million: 4
          cached_per_million: 0.5
```

Usage labels the resulting amount as an estimate, not an invoice. Missing model
rates, legacy/partial/active usage, or an unknown cache split with different
cached pricing keep the total unknown. A separately labeled known subtotal
prices only reported tokens with sufficient rate information. Cache tokens are
priced once within input. Remote agent consumption, non-token fees and taxes are
outside this local token estimate.

Project-specific role, budget and skill preferences are available through the
guarded [agent settings API](docs/api-contract.md#agent-settings).
They are saved privately in the canonical checkout's agent journal and apply to
future admissions. Budgets can be reduced up to the configured ceilings; role
choices reference the existing scopes, and skill controls can only disable or
reenable configured directories. Active runs retain their captured configuration.
Explicit request model choices remain authoritative.
Changing operator configuration makes saved overrides stale and requires an
explicit reset before new work; reset follows current configuration defaults.
The Web UI shows the effective roles and budgets in **Settings → Models &
agents**.

Each checkout has its own settings. A task created in its own worktree starts
with the settings saved in the checkout that created it (when they were saved
for the same configuration); other worktrees use configured budgets and skills
until explicitly edited. When the Web UI starts agents it turns the
checkout's role assignments into explicit workflow model choices. Remote workflow confirmation lists the selected role destinations and
captured paths; changing either invalidates that confirmation. Unknown model
metadata supplies no remote consent. Canceling a settings request may race a
completed save: refresh to establish the stored outcome.

### Scoped project skills

`agent_runtime.skills` is an explicit list of project-relative skill directories;
the default is an empty list. For example:

```yaml
agent_runtime:
  skills:
    - project-skills/review-code
```

Each directory must contain an indexed `SKILL.md` with valid ADK YAML frontmatter
and nonempty instructions. Its `name` must match the directory name. Configure
at most eight distinct, non-overlapping directories. Reindex after adding or
changing a skill. Excluded files and escaping symlinks cannot become skill context.
Loading configuration and browsing project state do not run skill tools.

Tool-capable Code profiles use the native ADK `list_skills`, `load_skill`, and
`load_skill_resource` tools during file explanation.
Only names and descriptions enter the initial prompt. Instructions and indexed
resources under `references/`, `assets/`, and `scripts/` load on demand. Completion-only
profiles retain their existing behavior and do not advertise these tools.

Each manifest/resource must be 1–8192 bytes of UTF-8 text; each skill can have up
to 16 resources, and the combined inventory is limited to 128 KiB. Descriptions
are at most 512 bytes; serialized metadata is at most 2048 bytes. The existing
16 KiB tool result, 128 KiB aggregate retrieval, model context, and shared run
budgets also apply. Oversized encoded results fail explicitly instead of truncating
instructions silently. Frontmatter `allowed-tools`, when supplied, must name only
the tools already admitted to that investigation; unsupported tools fail before
a model call.

Skill text is advisory and cannot override the task, repository instructions, or
grants. The Go SkillToolset has no script execution tool. Loading a script displays
its text; it cannot install packages, run commands, or write source. Explicitly
trusted fixed checks continue through the checked workflow's sandbox.

Manifests and loaded resources are checked against their captured hashes before
model dispatch and publication. Context manifests identify contributing skill
files, and private run tool records retain bounded file/hash provenance. Native
session events also record loaded-file identities. These source-bearing records
stay in the private journal and do not enter public progress events.
Conformance tests exercise native ADK tools with fake compatible and Google providers;
they do not establish live-provider performance.

### Optional context compaction and caching

All three optimizations are disabled by default. Configure them under
`agent_runtime.context`:

```yaml
agent_runtime:
  context:
    compaction: false
    token_threshold: 8000
    retain_events: 4
    summary_max_tokens: 1024
    local_cache: false
    cache_ttl_seconds: 600
    provider_cache: false
    provider_ttl_seconds: 0
```

Compaction uses ADK Go's native summarizer and token-threshold tail retention
inside investigations. In proposal conversations it summarizes older assistant
discussion while retaining every original user message and the recent tail.
Approved plans, acceptance criteria, source, instruction text and proposal
identity stay outside lossy summaries. The stored conversation remains intact.
The reconstructed request must still fit the selected context budget; oversized
mandatory material produces an error before summarization. Summaries are advisory,
and model suggestions never become verified findings through compaction.

`token_threshold` accepts 512–128000 estimated tokens; `retain_events` accepts
2–64; `summary_max_tokens` accepts 128–8192 and must be below the threshold.
Summary calls use the same admitted profile, source authority and shared budget,
including the investigation's eight-model-turn limit. They have a 30-second
timeout within the owning operation's deadline. A compaction or persistence
failure stops dispatch. No retry is hidden inside the summarizer.

`local_cache` reuses deterministic captured source composition for proposals.
Its TTL accepts 60–86400 seconds. Keys include project/revision, workspace and
source hashes, instructions, policy, schema and configuration. Current source
authority is checked again on every hit and model dispatch.

Private cache data and paid-operation receipts live in
`.mini-orca/agents/<workspace-hash>/context-cache.json` with mode `0600`.
The catalog permits 1024 records, 1 MiB per value and 32 MiB total. Expired local
composition is pruned during writes. Completed summaries remain reusable; an
uncertain summary or remote-cache creation remains pending across restarts and
cannot silently incur another call. A full or corrupt catalog fails explicitly.
To intentionally discard receipts, stop the affected workflow and remove that
checkout's cache file after reviewing the prior run; subsequent requests may pay
for summarization or cache creation again. Removing local metadata does not
delete provider-held content.

`provider_cache` enables a Mini-Orca adapter over the GenAI Go cache API; ADK Go
does not provide `ContextCacheConfig`. It requires an explicit
`provider_ttl_seconds` of 60–3600 and selected Gemini API or Vertex profiles in the
Gemini 2.5/3 Flash/Pro families. Unsupported profiles fail locally; model/account
availability remains subject to the provider. Gemini 4 and Interactions explicit
caching are unsupported. Existing profile-specific remote consent is still required.

Only a complete initial text turn of at least 16 KiB is eligible, once a request
also contains a subsequent turn and has captured source authority. Small or
single-turn requests proceed normally. Cached prefixes include their system/tool
configuration; resource identity, selected model, destination, expiry and source
authority are checked before reuse. Cache creation consumes an additional shared
model-call reservation; cached input still counts toward local context/token
limits. Failed or uncertain creation is not retried automatically. This does not
guarantee upstream cost savings or change a provider's implicit caching policy.
Explicitly cached source may remain remotely retained until the chosen
expiry even after local edits or forgetting; they cannot be reused under stale
local authority. Adapter behavior is covered by offline HTTP fixtures, not a live
provider billing benchmark.

## Other settings

```yaml
project_path: "."

retry:
  max_retries: 3
  backoff_base: 1000 # milliseconds
  backoff_max: 30000 # milliseconds

timeouts:
  import_seconds: 300
  analysis_seconds: 300
  generation_seconds: 300
  workflow_model_seconds: 900
  focused_check_seconds: 60

logging:
  level: "info"
  format: "json"
  filename: ""
  sensitive_keys: ["api_key", "password"]
```

The operation timeouts bound the request contexts; retries only cover explicit
prompt requests. Settings → Models & agents → Time limits (and the Agents &
budget step of setup) edit them for a project: they are saved in its
`.mini-orca/config.yaml` and applied at once. `analysis_seconds` bounds each
analysis request, `import_seconds` the project overview and other Analyze
model requests, `generation_seconds` each Code model request,
`workflow_model_seconds` each agent model call and `focused_check_seconds`
each check command. Restore, navigation, validation, checks, Apply, and Undo do
not make a model call.

`workflow_model_seconds` gives each Creation, Testing and Review model call at
least 15 minutes by default, including its provider retries. Longer generation
or selected-scope timeouts still take precedence. Existing configurations that
omit this field receive the new default; standalone generation retains its own
`generation_seconds` limit. Testing generates complete test files before running
checks, so its model budget is separate from each command's
`focused_check_seconds` limit.

The overall workflow deadline includes the remaining model budgets, check and
preparation allowances, and up to two repairs when explicitly enabled. It is at
least 30 minutes and grows with configured limits, subject to the shared
`agent_runtime.max_elapsed_seconds` cap. To adjust a slow workflow,
set `workflow_model_seconds`, restart the daemon, and run the agents again on the
saved task. A retry still validates the current source,
plan, consent and execution trust.

## Project configuration

A project can carry its own configuration in `.mini-orca/config.yaml`. It uses
the daemon's schema except `logging` and `project_path`, which configure the
daemon, and it is applied on top of the daemon's configuration: a model scope
it names (`model_scopes.analyze`, `bug` or `code`) replaces that scope whole, so
a provider change leaves no stale address or key behind, and every other key it
sets replaces the daemon's value for that key.

The first time a project without that file opens, the Web UI starts **setup**:

1. **Models** — provider, model, address, key, reasoning effort and CLI path for
   each scope, starting from the configuration in use, with a connection test
   (one short request, recorded as an agent run; no project code is sent).
   Saved keys are never shown again; leaving the field empty keeps them.
2. **Agents & budget** — the scope each agent role uses and the per-run budget
   (`agent_runtime.roles` and `max_*`). Saving here writes the file and resets
   any agent overrides saved in Settings → Models & agents, since setup has
   just set the values they deviated from.
3. **Checks** — the detected test and lint commands, to allow or not.
4. **Analysis** — folders to leave out of analysis, then optionally the first
   analysis.

Skipping keeps the daemon's configuration and records the choice in
`.mini-orca/setup.json`; Settings → Project shows where the configuration comes
from and reopens setup at any time. Setup rewrites only the keys it manages and
keeps any others, with their comments. The file is written `0600`, and
`.mini-orca/.gitignore` lists it so it stays out of Git.

The configuration applies without restarting: opening or switching to a
project loads its file, and saving setup reloads it at once. Saving is refused
while an analysis, an agent run or work in a worktree is running,
since that work was admitted with the old models. A file that cannot be applied
(invalid YAML, a daemon-only key, an invalid model) leaves the daemon's
configuration in use; the Inbox and setup show the error. Agent overrides saved
in Settings → Models & agents belong to the model configuration they were saved
with: after the models change some other way (editing a file by hand), analysis
and agent runs answer 409 until the overrides are reset there, and the Inbox,
the Analysis page and setup offer the reset. Connection tests in setup use the
configuration's own settings and are never held up. Importing a project
asks its analyze model for an overview only when that model is local or the
remote provider was confirmed; otherwise it records the deterministic inventory
and a later analysis writes the overview.

## Configuration changes

Only the current YAML schema is supported. For retired configurations, create a
fresh local file from `config.example.yaml`; unknown keys fail startup instead
of invoking compatibility fallbacks. `MINI_ORCA_CONFIG` selects the daemon's
file; a missing file then stops startup. Otherwise the daemon reads the first
`config.yaml` of `~/.config/mini-orca/`, the platform configuration
directory's `mini-orca/` (`~/Library/Application Support/mini-orca/` on macOS)
and the folder of the daemon binary, symlinks resolved. A `config.yaml` in the
project folder belongs to the project and is not read. With no daemon file, it
starts with local defaults (`http://localhost:1234/v1`) and logs the folders it
searched; `GET /api/models/current` reports the file in use as `config_file`
(the project's file when one is applied, empty for the defaults). `make
install` copies the repository's `config.yaml` to
`~/.config/mini-orca/config.yaml` when that file does not exist. Restart the
daemon after changing its own file; project files apply live.

## Local metadata and manual cleanup

Configuration stays separate from project metadata. Mini-Orca stores its local
index, analysis, findings, shared proposals and grouped recovery data
under the imported project's `.mini-orca/` directory; see
[README.md](README.md#project-intelligence) for the current paths. These files are
application metadata, separate from configuration.
Do not delete Apply receipts or Undo backups as a routine cache reset.

ADK session state, input history, final events, invocation status and shared budget
checkpoints live in `.mini-orca/agents/<checkout-hash>/journal.json`. The hash binds
the canonical checkout path; worktrees have separate records. Journals use atomic
replacement, Unix file/directory modes 0600/0700, and a process lock. Windows
access follows the checkout's filesystem ACLs. Input and response
history can contain project source, so keep this directory out of version control
and public diagnostics. No journal is uploaded automatically.

Budget reservations are saved before provider dispatch; usage and final events
must be saved before a successful result is published. A persistence failure stops
the shared run. Session app/user namespaces stay inside that checkout, temporary
state is not persisted, and partial streaming previews are not durable events.
Each journal is bounded to 64 MiB, 4096 sessions, 8192 invocations and 2048 budget
records. Corrupt, oversized or unknown-version data is preserved and rejected.
Opening a project reads records passively; it never resumes unfinished invocations
or grants source-write, remote-provider, or execution consent.

The current journal format is version 2 with `runtime_family: adk-go-v2`.
Version 1 journals remain readable without a write. The first admitted journal
mutation upgrades them atomically and marks abandoned `running` invocations
`interrupted`; valid waiting-input checkpoints, completed history and budget
charges remain intact. Unknown versions, mismatched runtime families and
ambiguous JSON are rejected without replacing the original data. A failed or
uncertain write cannot authorize another provider dispatch or replay Apply.
After restart, restore a proposal, explicitly trust and rerun its checks, then
review the current identity before Apply. Existing approval receipts retain
their identity but do not restore in-memory check authority. An artifact missing
its original project revision remains locally inspectable.

Versioned completion results and proposal/check evidence use the adjacent private
`artifacts.json` catalog. Each version records its SHA-256 hash, byte size, media
type, ownership and provenance. Content is limited to 4 MiB per version, 128
retained versions per artifact, 4096 artifact identities and 64 MiB per catalog.
Identical proposal/check snapshots reuse a version. A failed evidence write blocks
publication; saved artifacts never grant permission to Apply or Undo.

Artifact reads/export return verified bytes without accepting a project filesystem
destination. Deletion and retention are explicit, preserve version counters, and
reject evidence retained by an unfinished run or change recovery. Generic ADK
artifact operations cannot delete recovery-pinned proposal/check snapshots. There
is no automatic cleanup, source write or external upload.

Explicit file explanations use an ADK investigation with the selected
Code profile when it supports tools. Search, snippet, import and
heuristic impact tools admit only the selected file's source span and
indexed metadata. Other paths or line ranges return `scope_required`; they do
not widen consent. The current-check tool returns source-free, proposal-bound
check states; absent or stale evidence never implies a pass. It does not execute
checks. Completion-only CLI profiles continue using the supplied bounded context.

Each investigation retains its tool contracts, scope identity and tool events in
the private journal. Limits survive provider retries: eight model turns, 24 tool
calls, 16 KiB per tool payload and 128 KiB of aggregate tool output. Retrieval
returns at most 20 evidence items and 8 KiB of text, with file/hash/span provenance.
Source reads are capped at 1 MiB. Model context is capped at 128 KiB and the
Code profile's context token estimate (four bytes per token), whichever is
smaller. Source, policy, instructions and project identity are rechecked at
dispatch and publication; a changed scope stops the investigation.

Execution trust decisions are private user settings, outside project metadata.
They are stored in `mini-orca/execution-trust/` under the OS user configuration
directory (on macOS, `~/Library/Application Support/`). `MINI_ORCA_TRUST_DIR`
can select another absolute, user-controlled directory. Each decision is bound
to the canonical project path and command scope; copying `.mini-orca/` to another
checkout does not transfer consent. Grant or revoke trust in **Settings → Trust**.

Each launch writes a new write token to `mini-orca/daemon-token` in the same
user configuration directory, readable only by you (mode `0600`);
`MINI_ORCA_TOKEN_FILE` selects another absolute path. Requests that change
state send it in `X-Mini-Orca-Token`; the Web UI receives it in its page.
