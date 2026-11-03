# Kogin

A local-first coding assistant: a Go daemon on your machine that analyzes a
repository, surfaces verified and model-suggested **Issues**, writes structured
**Docs**, and prepares **AI changes** that agents write, test and review in an
Git worktree of their own, which you review in the browser and accept as a
commit on the task's branch.

> **Kogin was called Mini-Orca, and briefly Kasuri during v6 development.**
> The name comes from こぎん刺し (*kogin-zashi*, KOH-gin), a stitching
> technique from Tsugaru in northern Japan: geometric patterns built on woven
> cloth by counting its threads one by one, as agents fix one issue at a time
> and the work comes together only when you review and merge it. Projects,
> configuration and `KASURI_*` / `MINI_ORCA_*` variables from before the rename
> keep working: a project's `.kasuri/` or `.mini-orca/` moves to `.kogin/` the
> first time Kogin opens it. See the [release notes](RELEASE_NOTES.md) and the
> brand kit in [`brand/`](brand/README.md).

## What it does

- **Understand a project.** Deterministic indexing (`.kogin/index.json`,
  honoring `.koginignore`) feeds a resumable analysis across three model
  scopes (`analyze`, `bug`, `code`). Static rules and per-language security
  rule packs (Go, Python, JS/TS, Java, Kotlin, Ruby, PHP, C#, C/C++, Rust)
  produce verified findings with their lines; semantic, performance and
  security reviews produce labeled model suggestions. Each run then checks the
  project, in any language: parser diagnostics, files over their AGENTS.md code
  budgets, every subproject's lint and tests (when you let checks run project
  code) and semgrep with the project's own rules; failures become issues on
  their file and line, and close when fixed. The Docs page is a guide to
  the code in any language: what the project is for, how to run it, its
  components and the imports between them drawn as a diagram, walkthroughs of
  its main features step by step through the files, and a page per component
  with a line on each file. Each analysis run ends by summarizing again the
  components whose files changed and writing the overview from those
  summaries; coding agents start from the summaries around a task's files.
- **Change it safely.** A task names what should change, the files agents may
  write (up to eight) and "done when" criteria. A native ADK workflow writes the
  change, writes and runs tests in an isolated copy, and runs parallel specialist
  reviews within one shared budget; repairs are opt-in and capped at two.
- **Keep the code within its rules.** `AGENTS.md` files declare budgets (file
  lines, function lines and complexity), import boundaries and test file names;
  every task is checked against them in any language the parser knows (see
  [Code rules](#code-rules)). A change that pushes a file past its budget is split
  into sibling files in the same task; a file already over may change without
  growing and gets one refactor issue.
- **Work in its own worktree.** Every task gets its own branch and Git worktree
  from the main checkout. The proposal is that worktree's diff against the
  commit the task started from, so agents can add, rename and delete files;
  accepting it makes one commit on the task branch (one per issue for a task
  that fixes several), and Branches merges it. Kogin never writes a proposal
  into the main checkout.
- **Test every language it touches.** Each subproject's tests and lint come from
  its `AGENTS.md`, then its just/Task/make targets, then the ecosystem's defaults
  (Go, npm/pnpm/yarn/bun, Cargo, Maven/Gradle wrappers, pytest, .NET). Only the
  subprojects a task touches run; failures are read per test and compared with
  the tree before the task, so a test that was already failing does not block an
  unrelated fix. After a merge the same checks run on the merged result.
- **Stay in control.** Nothing reaches your files until you have viewed every
  changed file, required checks pass, the workflow waits for you and you approve
  that exact version, and only a merge from Branches brings it into your main
  checkout. Remote model use, check execution and merges each ask first.

## The Web UI

Start the daemon and open `http://127.0.0.1:9090/ui` (`just ui` opens it). The
browser workspace is built for three jobs: **start a task and watch the agents**,
**review and accept**, and **understand the project**.

| Page | Shortcut | What it is for |
| --- | --- | --- |
| **Inbox** | `g h` | What needs you now: tasks waiting for review, running agents, failures, branches ready to merge, open issues and analysis coverage. |
| **Tasks** | `g t` | Every task grouped by state (Needs you, Agents working, Failed or stale, Drafts, Done), with filters and `j`/`k` navigation. |
| **Task** | — | A pull-request style page: **Conversation** (brief, code rules per file, live agent timeline with stages and reviewers, revise box), **Files changed** (tree with viewed marks, unified/split diff, “since last review”), **Checks**, and a **Ready to apply?** gate with Apply, commit and Undo. |
| **Code** | `g c` | IDE-style explorer: virtualized tree, editor tabs, highlighted source, issue markers, line links, Ask about it and “New task with this file”. |
| **Issues** | `g i` | Findings by status and severity with evidence and **Fix with agents**, which opens the task the daemon drafts: the file, its conventional test in that language, and “done when” criteria. |
| **Docs** | `g d` | A guide to the project: what it is for, how to run it, the components diagram, walkthroughs of its main features step by step through the files, a page per component with its files, dependencies, and **Ask the code** (an agent answers with checked line citations). |
| **Branches** | `g b` | Task worktrees (each new task gets its own by default), merge preview (commits and diff), guarded merge or abort, the checks of the merged result, and removal of merged worktrees. |
| **Settings** | `g s` | Project, execution trust, models and agents, usage, appearance. |

`Ctrl+K` / `Cmd+K` opens the command palette (files, tasks, issues, commands);
`Ctrl+P` / `Cmd+P` jumps to a file; `c` creates a task; `?` lists every shortcut.
In **Files changed**, `j`/`k` (or `]`/`[`) move between files, `v` marks the
file viewed and `s` toggles split view. The checkout menu in the
top bar switches between the main checkout and agent worktrees; every page then
shows that checkout. Pages update live from the daemon's event stream. Light,
dark and system themes are available.

The task composer asks for a title, an optional description, the files agents
may change (up to eight; a companion test is suggested for Go, Python and
JavaScript/TypeScript files) and “done when” criteria. Every task gets a branch
and worktree of its own; there agents may also create, move and delete files,
repair failing checks up to twice, and **Accept & commit** commits the reviewed
diff on that branch.
**Start agents** runs the native ADK workflow (create → tests → specialist
reviews) after a confirmation that lists every remote model destination.
Accept is enabled only when every changed file is viewed at the current version,
required checks pass, the workflow is waiting for human review and you approved
this exact version. Accept, trust, merge and remote model requests always
ask for confirmation; navigation, previews and filters never start model work,
checks or writes.

## Code rules

Kogin reads the rules of a target project from its `AGENTS.md` files. A
fenced `kogin` block declares them; paths are relative to that file:

````markdown
```kogin
budgets:
  file_lines: 800          # test files too
  function_lines: 80
  function_complexity: 15  # cyclomatic; 15 when no guide sets it, 0 turns it off
imports:
  - from: internal/app
    never: [internal/api]
test_files:
  - "e2e/**/*.ts"          # added to the usual conventions (*_test.go, *.test.ts, test_*.py, …)
checks:
  test: pnpm test          # run in this file's directory; overrides make/just/npm detection
  lint: pnpm lint
```
````

- **Nearest wins.** An `AGENTS.md` in a subdirectory overrides the budgets it
  sets for the files below it; import rules and test file patterns add up from
  the root down. Guides without a block keep working through their
  plain-language lines (`Max file lines: 800`, `` `a` must never import `b` ``).
  A block that does not parse is reported on the task page and declares nothing.
- **Any language.** Functions are found and measured with go/ast for Go and a
  pure-Go tree-sitter runtime for the others (TypeScript, JavaScript, Python,
  Java, Kotlin, Rust, C#, C/C++, Ruby, PHP, Swift, Scala and more). Where the
  parser cannot outline functions, only file lines are checked and the gate says
  so; it never fails code it cannot read.
- **Ratchet.** The complexity gate compares each file with its version before
  the task: functions the task did not change never fail, and a file or function
  that was already over may stay over if it does not grow. Each such file gets
  one refactor issue (Issues → Maintainability), refreshed by later tasks.
- **Split in the same task.** The writing agents get each file's budgets and
  current numbers. When what they write still breaks a budget, a fix-up step
  (at most two rounds) moves cohesive code into up to two new sibling files per
  file, in the same directory, and Kogin checks that no function was lost
  and names the ones that changed while moving. The task page's **Code rules**
  card shows where every file stands.
- **Checks per subproject.** `checks` declares the test and lint commands of
  the subproject the `AGENTS.md` sits in (one command each, no shell syntax).
  Without it Kogin tests with the `test` target of a justfile, Taskfile or
  Makefile there, else the ecosystem's default, and lints with the ecosystem's
  own linter (`go vet ./...`, `cargo check --all-targets`, a package.json
  `lint` or `typecheck` script), never a runner's `lint` or `check` target,
  which often runs the tests too. The commands are part of what execution
  trust lists. A failing check is compared
  with the same command on the tree before the change: when every failing test
  (or lint finding) already failed there, the check passes as “only failures
  from before the change”; new ones are named and block.
- **Context for the agents.** Besides the task's files, the writing agents get
  the outlines (declarations and signatures) of the files those files import
  and of their neighbors, in any language the parser knows, so they reuse what
  exists.
- **Agents with tools.** On a model that runs in pi, Claude Code or agy, the
  coder and the testing agent of a task in its own worktree read and change the
  project through Kogin's tools instead of returning whole files. After each
  write a tool reports gofmt, parse errors and the file's budgets; `run_checks`
  runs these checks and the touched subprojects' tests on a copy. The agent ends
  with a report that maps its change to the acceptance criteria. Each CLI starts
  with its own tools, extensions, skills and context files off, and only
  Kogin's tool server; a call to any other tool stops the run, and a run that
  fails before finishing leaves the worktree as the stored proposal. A Claude
  model that calls one of Kogin's tools by its bare name (`read_file` for
  `mcp__kogin__read_file`) gets Claude Code's refusal and calls it again by
  its full name; the run stops at the fourth such call. The
  reviewers read the proposal as a diff.
- **Subscriptions, not API keys.** Each stage can run on a different
  subscription: Claude Code on your Claude plan, agy on your Google AI plan, pi
  on the ChatGPT plan (or a local model) it signs in with. Kogin removes API
  keys from Claude Code's environment and stops a run that would still bill one.
  When a plan reaches its usage limit, the task **pauses** at that stage instead
  of failing: nothing half done is kept, the task says when the limit resets, and
  **Resume agents** continues from the stage that stopped.

## Run locally

Use Go 1.27.3 or newer (development and container builds pin 1.27.3; CI reads
`go.mod`; `GOTOOLCHAIN=auto` downloads it). On macOS, Go 1.27 needs macOS 13+.
Read [CONFIG.md](CONFIG.md) before the first launch.

```sh
cp config.example.yaml config.yaml   # fill the three model scopes
just build                           # or: make install
cd ~/code/my-project && kogin    # opens this folder in the Web UI
```

`just build` copies `kogin` and `kogin-daemon` into `KOGIN_HOME`
(or `~/DEV/programs/kogin`); put that folder on your `PATH`. Run `kogin`
in a project folder, or pass the folder as its argument: it starts the daemon
beside it when none is running, opens that folder as the project and opens the
Web UI in your browser. The daemon it started runs until you press Ctrl+C, with
its log in that terminal. When a daemon is already running, `kogin` switches
it to the folder and exits. The first time a project opens, the Web UI walks
you through **setup**: its models (with a connection test), which model each
agent uses, the run budget, its checks and what analysis reads. Setup saves the
project's own `.kogin/config.yaml` (private, kept out of Git) and applies it
without a restart; skip it to use the daemon's configuration, which the daemon
reads from `KOGIN_CONFIG`, else `~/.config/kogin/config.yaml` (`just
build` copies the repository's `config.yaml` there once), the platform
configuration folder or the folder of the binaries. See [CONFIG.md](CONFIG.md).
`--no-open` skips the browser and `--url` (or `KOGIN_URL`)
targets a daemon at another address. Without the launcher, `go run ./cmd/daemon`
serves the folder it runs in at `http://127.0.0.1:9090/ui`.

On macOS, `just desktop` (or `make install-desktop`) builds **Kogin.app**
into `/Applications`: the same Web UI in a window of its own, with the daemon
started beside it and stopped when the app quits, and notifications when agents
or an analysis finish while you are elsewhere. See [DESKTOP.md](DESKTOP.md).

The daemon listens on `127.0.0.1:9090` only, unless `KOGIN_BIND_ADDRESS`
opts into another address (see [DOCKER.md](DOCKER.md)). Keep `config.yaml`
local. Every prompt-bearing request to a non-loopback HTTP or CLI provider asks
for consent for that scope. Requests that change state need the daemon's
per-launch write token: the Web UI has it, and other local clients read it from
`kogin/daemon-token` in your user configuration directory
(`KOGIN_TOKEN_FILE` selects another file). Running the daemon needs no Node.js: the Web UI
bundle is embedded in the binary.

<a id="project-intelligence"></a>

### Project data (`.kogin/`)

Project-local `.kogin/` stores `index.json`, `project-analysis.json`, `module-summaries.json`, `file-analysis/`, `findings.json`, `performance/files/`, and unified progress in `analysis/run.json`. Private `changes/` files retain conversations, captured source and proposals. Restore uses local persisted analysis without triggering model requests. See the [API guide](docs/api-contract.md) and [workflow migration](docs/api-contract.md#retired-workflows) for current contracts and boundaries.

## Web UI development

The UI is a React + TypeScript application in `web/`, built with Vite into
`internal/api/handlers/webui/dist`. The built bundle is committed and embedded
in the daemon. It is served with a strict Content-Security-Policy: same-origin
scripts, styles and fonts only, no inline scripts, and one per-response nonce
for the style tag dialogs inject.

```sh
make webui-install     # npm ci in web/
make webui-dev         # hot reload on :5173, API proxied to the daemon on :9090
make webui-test        # type-check + unit and component tests (vitest)
make webui-e2e         # Playwright browser tests against a fake daemon
make webui-build       # rebuild the embedded bundle; commit the dist/ changes
make webui-dist-check  # fail if the committed bundle is out of date
```

The first `make webui-e2e` on a machine needs `npx playwright install chromium`
in `web/`.

## Architecture

| Layer / Path | Responsibility |
| --- | --- |
| `cmd/daemon` | The daemon: loopback HTTP API, embedded Web UI, startup and shutdown. |
| `cmd/kogin`, `internal/launcher` | The launcher: attach to or start the daemon for a project folder; shared with the desktop app. |
| `desktop/` | Kogin.app: the Web UI in a native window (Wails v3, its own Go module, [guide](DESKTOP.md)). |
| `cmd/agent-inspect` | Read-only inspection of local agent invocation history ([guide](docs/agent-inspect.md)). |
| `cmd/agent-eval`, `cmd/engineering-insight-eval` | Synthetic agent task/trajectory evaluation and engineering-insight evaluation. |
| `web/` | The browser workspace (React + TypeScript), built into `internal/api/handlers/webui/dist`. |
| `internal/api` | HTTP decoding, local request and browser-origin policy, structured errors. |
| `internal/app` | Admission, consent, workflow hosts, analysis, Git isolation and guarded source mutation. |
| `internal/app/agentruntime` | ADK Go agents, model adapters, graphs, tools and plugins; the only package importing the SDK. |
| `internal/app/agentstate` | Private durable sessions, invocations, budgets, checkpoints and artifacts. |
| `internal/project` | Repository facts, indexing, context composition, the docs and `AGENTS.md` instructions. |
| `internal/project/rules`, `internal/project/syntax` | Code rules from `AGENTS.md`; functions, spans and complexity in any supported language. |
| `internal/storage` | Atomic durable file primitives under `.kogin/`; domain packages own their schemas. |
| `internal/llm`, `internal/config` | HTTP/CLI/native Google transport, provider capabilities and the three model scopes. |
| `internal/agenteval`, `internal/insighteval` | Evaluation harnesses, separate from ordinary use. |

## Main dependencies

| Dependency | Purpose |
| --- | --- |
| **Go 1.27.3+** (`gopkg.in/yaml.v3`) | Daemon, indexing, analysis orchestration and atomic `.kogin/` storage. |
| **Google ADK Go v2.6.0 / GenAI Go v1.74.0** | Typed model and tool events, native graphs and sessions. |
| **gotreesitter v0.56.0** | Pure-Go tree-sitter runtime (no CGO) with 206 embedded grammars for language-neutral syntax checks and function budgets. |
| **Model providers** (OpenAI-compatible HTTP, Gemini, Vertex, `agy`, `pi`, `claude`) | Explicit local or remote execution for `analyze`, `bug` and `code`; the CLI profiles also run the tool-using agents. |
| **React 19, TanStack Query, Radix UI, Vite** | The browser workspace. |
| **Wails v3.1** (desktop module only) | The desktop app's native window, menus and dialogs, on the system WebView. |

## Agent capabilities

Capabilities are admitted for the configured provider and the exact operation;
no provider fallback occurs. See [CONFIG.md](CONFIG.md), the
[API contract](docs/api-contract.md), the [UI guidelines](docs/UI_DESIGN_GUIDELINES.md)
and the [UI behavior contract](docs/ui-behavior.md).

| Capability | Delivered behavior and boundary |
| --- | --- |
| Models and streaming | HTTP and native Gemini/Vertex adapters preserve tool identities, schemas, cancellation and usage. CLI profiles (pi, Claude Code, agy) provide completion and also run the tool-using coder and tester (below). Partial output never becomes an approved proposal. |
| Plan limits | A CLI that reports its subscription plan's usage limit is not retried: the workflow pauses at that stage with the CLI and, when it said so, the reset time, and resumes from that stage. |
| Task tools | A task in its own worktree whose creation or testing model runs in pi, Claude Code or agy gets a tool-using agent for that stage. Its only tools are Kogin's, served by a per-run MCP server on a loopback port with a one-time token: read, list, search, outline, definitions and references, edit by exact text, create, delete, rename, run the checks, finish. Every call passes the context policy and the task's diff policy; the testing agent writes test files only. The worktree's diff becomes the proposal. |
| Repository tools | The agent evaluation's investigations use captured snippets, imports, search and impact evidence. Tools cannot widen their admitted scope or write source. |
| Workflow graphs | Sequential stages, parallel specialist reviews, conditional branches, bounded repairs and durable human-input checkpoints run through ADK with host-owned checks and budgets. |
| Sessions and artifacts | Private local journals and versioned evidence; restart keeps history and charges, not execution authority. |
| Context | Compaction and context caching are optional; remote cache retention needs consent. |
| Observability | Local source-free traces, reported and estimated usage and optional versioned cost estimates. Missing usage and unknown cost stay unknown. |

Unrestricted shell tools, implicit credentials, automatic source writes and
automatic merges are outside the supported execution boundary. Conformance and
integration tests use fake providers and temporary projects: they establish
transport, lifecycle and guard behavior, not live model quality or cost.

## Development and documentation

Follow [AGENTS.md](AGENTS.md). The full local gate is:

```sh
./scripts/validate.sh
```

It runs formatting, Go unit/race/vet tests, the pinned quality checks, Python
tooling tests and the Web UI unit, browser and bundle checks. It uses fakes and
temporary directories, starts no daemon and needs no provider, key or project
configuration. `make check` runs the same gates through make; `make quality`
runs the pinned static, reachability, complexity and clone checks.

### Evaluation harness

The native Go agent evaluation harness runs a fixed, versioned synthetic corpus
through the production ADK runner and guarded repository tools:

```sh
go run ./cmd/agent-eval
go run ./cmd/agent-eval --case addition
```

The default replay mode uses an owned loopback fake provider. It reads no
workspace files and writes no proposals or runtime stores. Four task cases cover
arithmetic, comparisons and a bounded repair; five negative controls verify
scope violations, unsupported check claims, wrong proposals, incomplete output
and cancellation. The harness independently checks proposed Go return expressions
against held-out inputs using Go's constant evaluator. It never executes generated
code, arbitrary shell commands or project tests. This small corpus measures these
specific behaviors; it is not evidence of general coding quality.

JSON receipts on stdout identify corpus version/digest, prompt and scoring
versions, linked ADK/Go/build identity, hashed model/profile, budgets, tool
trajectory correctness, scope violations, unsupported claims, regression results,
repairs, token charges/reported usage and elapsed time. Missing usage stays
unknown; canceled/incomplete latency is labeled and unrun cases have no invented
measurements. Replay token usage is simulated and timing measures the local
harness. A replay campaign passes when task cases pass and negative controls are
detected; control cases retain their actual failed/incomplete/canceled states.
Receipts omit source, paths, prompts, model answers, credentials and raw errors.
Unavailable build metadata remains labeled `unavailable`.

The **coder suite** compares the tool-using coder with the one-shot proposal on
five small tasks in Go, Python and JavaScript (a fix, a move into a new file, a
deletion, a feature). Each coder runs the creation stage once on the **bug**
profile's model, in its own worktree of a scratch repository; the suite then adds
the task's hidden tests and runs its test command. The receipt keeps each case's
state, failing test names, changed files, model calls, tokens and tool calls, totals
per coder, and whether the tool-using coder passed at least as many tasks (exit
status 1 when not). It needs `go`, `python3` and `node` for the tasks, a pi,
Claude Code or agy model for the tool-using coder, and runs live only:

```sh
go run ./cmd/agent-eval --suite coder --mode live --config config.yaml --confirm-live
go run ./cmd/agent-eval --suite coder --mode live --config config.yaml --case py-slugify --coders tools --confirm-live
```

To check that the pi installed on your machine reaches only Kogin's tools,
without using your plan, run it against a scripted local model:
`KOGIN_PI_SMOKE=1 go test ./internal/app -run TestRealPi -v`. Claude Code
and agy cannot use a local model, so their checks use a little of your plan for
one small run each:
`KOGIN_CLAUDE_SMOKE=1 go test ./internal/app -run TestRealClaude -v` and
`KOGIN_AGY_SMOKE=<model slug from agy models> go test ./internal/app -run TestRealAgy -v`.

Live mode sends only the embedded synthetic corpus to the configured **code**
profile and excludes negative controls. It requires an explicit command and
campaign limits. Omitting `--confirm-live` prints the source/destination disclosure
without sending a request:

```sh
go run ./cmd/agent-eval --mode live --config config.yaml --case addition \
  --requests 8 --input-tokens 65536 --output-tokens 8192 --confirm-live
```

Live evaluation uses one shared campaign budget, no provider fallback or hidden
retry, at most two admitted repairs, and a 60-second campaign deadline. It
supports configured HTTP providers with native tool support; CLI providers are
unavailable for this harness. The selected profile must allow 1–4096 output
tokens per call. Exit status is `0` for a passed campaign, `1` for a
failed/incomplete campaign with a receipt, and `2` for invalid configuration or
receipt output failure. Ctrl-C stops requests and emits an incomplete receipt.
No live evaluation is run by tests or CI.

- [UI guidelines](docs/UI_DESIGN_GUIDELINES.md) · [UI behavior](docs/ui-behavior.md) · [design direction](docs/design/ui-directions.md)
- [API guide](docs/api-contract.md) · [OpenAPI](docs/openapi.yaml) · [configuration](CONFIG.md)
- [Desktop app](DESKTOP.md) · [Docker](DOCKER.md) · [agent inspection](docs/agent-inspect.md) · [release notes](RELEASE_NOTES.md) · [LICENSE](LICENSE)
