# Kogin release notes

Kogin was called Mini-Orca until v6 (and Kasuri for part of v6 development); the notes for earlier versions keep the old name.

## v6.0.0-dev — In development

Version 6 starts from the stable v5.0.0 tag on the `v6` branch. Its changes
are listed here as they land.

- **Mini-Orca is now Kogin** (こぎん刺し, *kogin-zashi*: geometric patterns
  stitched on woven cloth by counting its threads one by one). During v6
  development it was briefly called Kasuri. New name, woven logo, app icon and
  a default **Kogin** palette (midnight violet; emerald for actions, violet for
  navigation; Midnight stays in Settings → Appearance), and the brand kit in
  `brand/`. Commands are `kogin` and `kogin-daemon`, the app is Kogin.app
  (`io.github.nanaki-93.kogin`, so macOS asks again to allow notifications).
  What was made under the old names keeps working:
  - a project's `.kasuri/` or `.mini-orca/` folder moves to `.kogin/` the first
    time Kogin opens the project, and `.kasuriignore` and `.mini-orcaignore` are
    still read next to `.koginignore`;
  - `~/.config/kasuri/config.yaml` and `~/.config/mini-orca/config.yaml` are
    still found (after `~/.config/kogin/config.yaml`), and the settings folder
    in the platform configuration directory moves from `kasuri` or `mini-orca`
    to `kogin`;
  - every `KASURI_*` and `MINI_ORCA_*` environment variable still works when its
    `KOGIN_*` name is not set;
  - Web UI preferences carry over; a dark palette saved as Kasuri becomes Kogin,
    and preferences from Mini-Orca start on the Kogin palette;
  - `make install-desktop` removes Kasuri.app and Mini-Orca.app.
  The repository is now `github.com/nanaki-93/kogin` (the old URLs redirect),
  and the Go module path is `github.com/nanaki-93/kogin/v2`.
- **Docs, shorter and linked to the code:** the overview (prompt
  `project-analysis-v8`) is terse: purpose in one or two sentences,
  architecture as one `path: role` line per main part, shown as a list,
  walkthrough steps of a few words; component summaries (`module-summary-v3`)
  are one sentence, without responsibilities that repeat it. New **Where to
  change** pages: the changes you most likely make to the project (such as
  "Add a subcommand"), each with the files and functions to touch in order,
  checked against the code like the walkthroughs. File paths, components and
  backticked names in the docs' prose open the code (or the component's page).
  One **AI** mark per page instead of one per paragraph, and the explanatory
  notes under the page titles are gone. Refresh docs (or run an analysis) to
  get the new overview; until then the old one shows, marked out of date.
- **Desktop app (macOS):** `just desktop` builds Kogin.app into
  `/Applications`: the Web UI in a window of its own. It reopens the last
  project (or asks for one), starts the daemon beside it with your login
  shell's `PATH`, or uses one that is already running, and stops the daemon it
  started when it quits. File → Open Project… switches projects; the daemon's
  log is in `~/Library/Logs/Kogin`. Built with Wails v3 in `desktop/`, a Go
  module of its own. See [DESKTOP.md](DESKTOP.md).
- **Desktop notifications:** while its window is not in front, the app tells
  you when agents finish a task (ready for review, changes requested, failed,
  out of date, paused) or an analysis run ends; clicking opens that page.
  Analysis events now carry the run's ID and status, and a task's events from
  its worktree carry `worktree_path`.
- **Fixed:** a task fixing several issues of one file could fail its review
  with "conversation context exceeds 256 KiB": the prompt carried each changed
  file twice (as captured and as proposed) and the whole conversation. A
  changed file is now carried once, as proposed, and a stage gets at most
  48 KiB of the conversation: the task as first asked and the newest messages.
- **Smaller grouped fixes:** **Fix N together** drafts one task for up to five
  issues of a file (was ten), since tasks fixing ten at once often failed or
  came back with blocking review findings. A file with more open issues offers
  **Fix 5 of N together**; the rest can be grouped once those are in progress.
- **Every task in its own worktree:** the composer no longer offers to keep a
  task in the main checkout: every task gets its own branch and worktree from
  the main checkout's HEAD, even while a worktree is selected, and the composer
  says what is missing (Git, a commit, trust) when one cannot be made. The
  daemon creates no other kind (`POST /api/projects/current/changes` has no
  `worktree` field and refuses a request scoped to a worktree).
- **Removed: Apply and Undo in the main checkout.** Accepting a task commits it
  on its branch; Kogin never writes a proposal into the main checkout, so the
  Undo button, `POST .../changes/{id}/undo`, `GET .../changes/recovery` and the
  recovery journal (`.kogin/changes/last-mutation.json`, which can be deleted)
  are gone, and Accept takes no `head`/`branch`. Tasks made in the main checkout
  before this open read only and can be deleted.
- **Simpler composer:** no task kind picker (a new task is a feature; one
  drafted from an issue keeps its issue's kind) and no repair checkbox (repairs
  are always on, up to two). One-shot proposals in a worktree are gofmt-formatted
  like the agents' edits.
- **Issues fixed one at a time:** a **Fix N together** task in its own
  worktree no longer fixes all its issues in one run. Each issue gets a fresh
  coding run with only that issue (fix plus regression test), then the checks,
  with one repair; a fixed issue becomes a commit, `fix: <issue>`. An issue it
  cannot fix (checks still failing, nothing changed because it does not hold or
  needs a task of its own, or out of time) is rolled back, marked **Not fixed**
  with the reason and opens again in Issues, and the next issue starts. Testing
  and review then run once on the whole branch; a review repair acts only on
  the feedback. The task page lists each issue's step, and accepting keeps one
  commit per fixed issue, so merging marks only those fixed. When the run stops
  in the middle of an issue (checks that did not complete, a model error, a
  Stop), that issue's step is rolled back, so the next run starts it over from
  the last fixed issue instead of finding its edits already made and marking
  it **Not fixed** with "the coding agent changed nothing".
- **Tasks that touch the same code take turns:** a run that would change code
  another open task is changing waits for it ("Waiting for …", with a link),
  at its start or when a fix asks for such a file. When that task is merged or
  deleted, the waiting one continues by itself on the updated code: Kogin
  opens it again in a new worktree, carries over its fixed issues commit by
  commit while they still apply (the agents redo the rest) and starts its
  agents with the same request. After a merge it first waits for checks to be
  allowed again.
- **The testing and coding agents no longer undo each other:** the tests the
  testing agent writes are its own; a coding repair may not change them. A
  coder sure that one is wrong disputes it once, with its reason, and the next
  testing run decides; that decision stands. Behavior the change cannot reach
  becomes a suggested follow-up (with **New task**) instead of a failing test.
- **Nothing to fix:** a run in which no issue was fixed and some do not hold
  ends **Nothing to fix**, not failed. **Close task** dismisses those issues
  with their reasons and removes the task and its worktree.
- **Issue steps say why they changed nothing:** a step that changes nothing
  must end saying the issue is already fixed, does not hold or needs a task of
  its own; a finish without that is refused, so the agent says it in the same
  run instead of the issue ending Not fixed and going back to Issues. Already
  fixed counts as fixed by the earlier step's commit; what a step that says so
  edited anyway is rolled back.
- **After the steps, a task is what it fixed:** testing, review and repairs
  read only the fixed issues' acceptance criteria and are told which issues
  were left out, so they no longer write tests for, review against or repair
  toward an issue that does not hold or was not fixed.
- **Repairs read what failed:** the check failures a repair gets leave out
  optional checks (such as a regression baseline the old code cannot build)
  and lead with the lines that say what failed, before the end of the output;
  when a Go test binary exits before any test reports a failure, the repair is
  told so. The agents' own `run_checks` output reads the same way.
- **Fewer repeated issues after a run:** when the file, Security and
  Performance analyses report the same problem in one file, it is one issue
  (the most severe), listing the others under **Also reported**. An issue
  that comes back in new words after you dismissed it, or after a task found
  it does not hold, is dismissed again with a note saying which one it
  repeats (reopen it if it is different).
- **Test quality apart:** Issues has **Code** and **Test quality**.
  Low-severity issues in test files are listed under Test quality only, are
  not counted with the open issues, and are not offered in **Fix N together**,
  so fixes that add tests no longer feed a stream of test nitpicks.
- **Issue steps check faster:** while a task fixes its issues one at a time,
  each step (and its agent's `run_checks`) runs only the tests of the Go
  packages the change touches, without the project's test command or flags
  such as `-race`; the whole suite still runs once at the Checks stage, and
  only that run lets the task be accepted. A quick check that fails falls back
  to the project's tests. The before-the-change comparison of new tests runs
  only their packages, and stops at once when they cannot build on the old
  code.
- **A fix can reach the code it calls:** a task that fixes issues no longer
  stops when the fix belongs in code its files import (a fix in
  `cmd/master.go` that needs `internal/utils`). Its coding agent asks for those
  files, says why, and Kogin allows them when the task's code imports them:
  at most two more folders and eight code files in all, never files the
  project's context policy leaves out. The task page lists them as **Added to
  the fix**, with the reason, and the reviewers judge the wider change.
  Anything further is still refused, and the issue is left for a task of its
  own.
- **Each issue step says how it ended:** an issue an earlier step of the task
  already fixed is now **Fixed** with that step's commit (it used to be marked
  Not fixed and open again), and one that does not hold is **Does not hold**,
  with the agent's reason: it stays with the task and merging the task
  dismisses it (you can reopen it in Issues).
- **Go on with another model when a plan runs out:** resuming a run paused at
  a plan's usage limit now offers **Models for this run**: the model of each
  stage that runs, starting from the roles, with the plan that ran out marked
  "limit reached". Pick another (a configured scope or a model Claude Code,
  agy or pi lists) and the run continues now instead of after the reset. The
  stages it keeps keep their model; the roles in Settings stay as they are.
- **Why the agents could not start:** when a run is refused because something
  changed, the message now says what: for example that the selected model
  (`agy:gemini-4-argon-low`) is no longer offered by its CLI and another must be
  chosen in Settings → Models & agents. It used to say only "project or file
  changed; refresh the proposal", which no refresh could fix.
- **Kogin lints with the language's own linter:** the lint check is
  `go vet ./...` for Go, `cargo check --all-targets` for Rust and a
  package.json `lint` or `typecheck` script for Node, no longer a `lint`, `vet`
  or `check` target of the project's justfile, Taskfile or Makefile (nor a
  package.json `check` script): those often run the whole test suite too, so
  every check pass ran the tests twice and timed out. Tests still use the
  runner's `test` target. An AGENTS.md checks block can still declare either
  command.
- **Fixes stay focused:** three changes for tasks that fix issues, which often
  failed or ran long without a result.
  - A fix changes code only in the folders of its issues' files and of the
    files the task names (test files anywhere). A write elsewhere is refused
    and the agent is told to stop and say the issue needs a task of its own;
    its words show in the failure reason.
  - The testing step no longer fails with "the testing agent wrote no tests"
    when the coder already wrote them: the testing agent checks them, adds
    only what they miss, and the checks run them as before.
  - Reviewers block only on defects the change introduces or criteria it
    leaves unmet; problems the code had before, and improvements beyond the
    task, become notes.
- **Delete set-aside files:** Settings → Project now has **Delete** on each
  file Kogin set aside because it could not read it (an agent journal, task
  file or saved analysis progress): after a confirmation it deletes the file
  and its record (`POST /api/projects/current/quarantine/delete`), so the Inbox
  notice goes away. Records made before the rename name their `.mini-orca/`
  paths where the files are now, in `.kogin/`, with Kogin's remedy, and a
  record whose file was deleted by hand is no longer listed.
- **Short review findings:** reviewers write each finding and note as a few
  words with the file and a quoted line; the daemon finds the line, and the
  task page shows `master.go:42` as a link to that line in the diff, with the
  reason behind **Why**. Reviews saved with text findings still read.
- The `kogin` launcher and the desktop app share `internal/launcher`.

## v5.0.0 — Web-first workspace

The browser workspace served by the daemon at `/ui` is now Mini-Orca's only
interface.

- **Removed:** the terminal UI (`cmd/tui`, `internal/tui`), the remote agent
  worker (`cmd/agent-worker`), and the features only the terminal UI exposed:
  missions, live sessions, cloud copies, remote agents and delegations, the
  inbound A2A listener, MCP and OpenAPI integrations with API actions, grounded
  research, ambient triggers, attachments, the artifact library and curated
  memory. Their routes return 404; the `integrations` section and
  `agent_runtime.live_model`/`triggers` keys now fail startup. See
  [retired workflows](docs/api-contract.md#retired-workflows).
- **Web UI:** a calmer visual system (inset content panel, breadcrumbs, aligned
  task columns), an Inbox summary, required-versus-optional check results in the
  review gate, explained merge availability on Branches, and accurate finding
  source labels. A Playwright suite (`make webui-e2e`) runs the real UI against a
  fake daemon.
- **Review:** you have the last word. Specialists separate blocking findings
  from advisory notes, a rejected version can be overridden with a reason
  (`POST …/review-override`, bound to its hash and approving nothing), and
  approval accepts notes kept with that approval (`review_notes`).
- **Freshness:** a task goes stale only when a file it depends on changes (its
  targets and read context, the rest of the target packages, build manifests
  above them; `.gitignore`d files never count), and it names that file. Changes
  elsewhere keep it current and only mark its checks outdated. Apply, Undo,
  merges, branch switches and reindexing stop only the running workflows whose
  files they change, and say which file; the task list now carries each task's
  paths.
- **Line comments:** comment on any line of a changed file; open comments go
  to the agents as one structured repair brief with the next revision or run
  (`include_comments`) and stay as history once sent.
- **Usage:** each workflow stage records what it spent, the task page shows
  per-stage and per-task tokens and estimated cost, and Settings → Usage lists
  spend per task (`GET /api/projects/current/usage`). Live events now name
  the task, stage, status and spend, refresh only that task, announce finished
  runs, and reindexing is published.
- **Quarantine:** an unreadable agent journal or task file is set aside
  (bytes kept beside it) instead of blocking the checkout; files from a newer
  Mini-Orca are refused or skipped with an upgrade remedy; journal errors name
  the file. `GET /api/projects/current/quarantine` and the Inbox report them.
- **Write token:** every request that changes state must carry the daemon's
  per-launch token (`X-Mini-Orca-Token`). The Web UI gets it in its document;
  other local clients read the `0600` token file (`MINI_ORCA_TOKEN_FILE`, or
  `mini-orca/daemon-token` in the user configuration directory).
- **Agent models:** Settings → Models & agents now edits the model each agent
  role runs with — a configured scope or any model the Agy or Pi CLI lists — and
  the budget per run, saved per project for new runs, with a reset to
  `config.yaml`. Roles accept catalog model IDs (`POST
  …/agent-settings/control`), and agent settings report `role_models`.
- **Fixed:** a daemon started by `mini-orca` from an install folder without a
  `config.yaml` silently used the local defaults (`localhost:1234`), so every
  analysis stage failed. The daemon now also reads `~/.config/mini-orca/` and
  the platform configuration directory (symlinked binaries resolved), `make
  install` seeds `~/.config/mini-orca/config.yaml`, the launcher prints the file
  it uses, and the status bar and Settings warn when there is none. A missing
  `MINI_ORCA_CONFIG` file now stops startup.
- **Project configuration and setup:** each project can have its own
  `.mini-orca/config.yaml`, applied on top of the daemon's and live — on
  opening or switching projects and on saving, without a restart. The first
  time a project without one opens, a setup wizard configures its models (with
  a connection test), the model each agent uses, the run budget, its checks and
  what analysis reads; skipping keeps the daemon's configuration. Keys are never
  sent back to the browser, the file is private and kept out of Git, and setup
  keeps the file's other keys and comments (`GET/POST
  /api/projects/current/setup`, `…/setup/skip`, `…/setup/test-model`). Import no
  longer fails for a remote analyze model without consent: it records the
  inventory and a later analysis writes the overview. A `config.yaml` in the
  project folder is no longer read as the daemon's configuration.
- **Fixed:** after setup, connection tests and analysis failed with "The
  active project changed" because agent overrides saved in Settings belonged to
  the previous models. Saving setup now resets them, connection tests ignore
  them, and the remaining case names Settings → Models & agents and offers
  **Reset** in the Inbox, the Analysis page and setup.
- **Faster triage:** Issues move with `j`/`k`, fix with `f`, dismiss or reopen
  with `x`, and filter by kind; the status bar shows a running analysis and the
  code inspector shows each file's analysis stages.
- **Dark palettes:** Midnight, Graphite, Ocean, Forest, Plum, Ember and True
  black, chosen in Settings → Appearance or the command palette.
- **Delete tasks:** stale, failed and draft tasks can be deleted one at a time
  or per group (`POST …/changes/{id}/delete`); running tasks and the task
  Undo still needs are kept.
- **Analysis dashboard** (`g a`): live progress of the last or running analysis,
  failed stages grouped by reason, coverage and findings per section, models
  used and every file's stages. It offers one next step (Analyze changes, or
  Retry failed when stages failed) with the other runs under **More**, plus
  pause, resume and cancel. Issues filter by category. An analysis window now
  runs up to 30 minutes before pausing.
- **Analysis failures say why:** a failed stage names an unreachable provider, a
  timeout, a rejected request or an unusable answer instead of one generic
  message.
- **Docs:** a docs site with Overview, Get started, Architecture, How it works
  (a walkthrough per main feature, each step at a file and function), a page
  per component, Dependencies and Ask the code. The overview
  (`project-analysis-v7`) and component summaries (`module-summary-v2`) are
  short; **Refresh docs** writes them again, and a failed rewrite keeps the
  saved overview. Older overviews still load, marked out of date.
- **Issues by file:** Issues are grouped by file by default (**By severity**
  lists them flat). A file with several open issues offers **Fix N together**:
  one task for up to ten of them (`GET /api/projects/current/findings/task`)
  that marks them all fixed when it is merged.
- **One home per action:** analysis runs only from the Analysis page
  (`/settings/analysis` opens it; Settings no longer has Reindex), and New
  task lives in the sidebar and on `n`.
- **Removed:** file explanation (`POST /api/projects/current/files/explanation`)
  and the project skills only it loaded. The Code inspector's **Ask about it**
  opens Ask the code with a question about the file. `agent_runtime.skills: []`
  still loads, a list of skills is refused with a message to delete it, and
  saved agent settings with `disabled_skills` still load.
- **Launcher:** `mini-orca` (built and installed by `just build`) opens the
  project in the current folder: it starts the daemon when none is running,
  switches it to the folder and opens the Web UI; Ctrl+C stops a daemon it
  started.
- **Fixed:** a daemon with no `config.yaml` failed to start instead of using
  local defaults.
- **Fixed:** unreadable saved analysis progress (for example from an older
  Mini-Orca) made the overview and every analysis preview fail with 400
  "Review the request and try again". It is now set aside like other
  unreadable records, and error messages in the Web UI include the daemon's
  detail.
- **Fixed:** a Go scan canceled while its workspace was being prepared is
  reported as canceled, not failed.
- Model capabilities report completion, JSON Schema, tools and streaming only.

## v4.4.0 — Focused local TUI and daemon workflow

Mini-Orca pairs a pure Go Terminal UI (`cmd/tui`) and loopback daemon (`cmd/daemon`)
with scoped model profiles, unified analysis, Security rules and advisory
Performance review. Chat prepares checked proposals across captured Go/Markdown
files with explicit Apply and guarded grouped Undo. Legacy desktop clients,
declaration editing APIs, draft benchmarks and separate legacy analysis jobs have
been removed. See [workflow migration](docs/api-contract.md#retired-workflows),
[README.md](README.md) for current usage and [CONFIG.md](CONFIG.md) for setup.
