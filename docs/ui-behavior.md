# UI behavior contract

This is the behavior contract for the workflows the Web UI drives: what each
action may do, what it must ask first, and which states keep their meaning. It
is independent of layout; [UI_DESIGN_GUIDELINES.md](UI_DESIGN_GUIDELINES.md)
owns how pages look and are navigated. The daemon remains the authority: the UI
offers an action only when daemon state allows it, and the daemon re-checks
every guard when the action arrives.

## Shared rules

- Navigation, selection, filters, previews, disclosures and the command palette
  never dispatch a model, execute project code or write source.
- Every mutation carries the identity it was decided on (project, revision,
  proposal revision and hash); a changed identity is rejected and the UI shows
  the daemon's message instead of retrying with fresh data.
- Remote model use, check execution, Apply, Undo, merges, trust and stopping
  agents each ask first and name their exact effect. A request is never sent
  twice behind the user's back, and one mutation runs at a time.
- Unknown, unavailable, stale, partial, failed and canceled states keep their
  meaning; model suggestions are never shown as verified results.
- Pages follow the daemon's event stream; when it drops, the status bar says
  so and the stream reconnects with backoff.

## Tasks

- **Create.** A task needs a title; scope (up to eight files) and "done when"
  criteria are explicit. A file that does not exist yet joins the scope by
  typing its path (**New file**); agents can write only the files in scope,
  and a run whose proposal writes another file fails naming it. **Save
  draft** stores it without model work.
- **Own worktree.** From the main checkout a new task gets its own branch
  (`orca/<title>-<suffix>`) and worktree from HEAD by default (**Work in its
  own worktree**), so several tasks run, apply and commit in parallel; the task
  then opens in that checkout. It needs Git, a commit and trust, and the
  task's files must be committed (the daemon names a file that is not).
  Unticking it, or creating a task while a worktree is selected, keeps the task
  in the selected checkout. The worktree runs checks under this checkout's
  trust when its commands are the same, and with the agent roles and budget
  saved here; otherwise a warning says what to set. A task page shows its own
  checkout, but the workspace pages (sidebar, `g` keys and the Tasks crumb)
  keep the checkout chosen in the top bar, so opening a task in its own
  worktree does not switch Issues or Analysis to it.
- **Start agents.** Requires a trusted project, and a test file in scope unless
  the task has its own worktree (there a testing agent on a CLI model creates
  the test files it needs; the daemon refuses the run when the testing model
  cannot). When something is missing, the composer says what instead of
  leaving the button silently disabled. The
  confirmation lists every remote destination the Creation, Testing and Review
  roles will use; local-only runs say so. Repairs are opt-in and capped at two.
  A security task adds a security specialist.
- **Run again.** Whenever no run is active and the task is not applied, the
  task page offers **Run agents again** (beside the review step, or as the
  primary action when the agents must run before human review, and on a run
  that ended); when the daemon would refuse a run, the button stays visible,
  disabled, and says why. With a proposal, the confirmation asks where to start:
  **Check and review this version** keeps the code and tests as they are,
  including a version revised in the Conversation, and runs the checks, then
  the reviewers (chosen first when the proposal has tests); **Write new tests**
  keeps the implementation; **Write the change again** has the creation agent
  revise it. Starts that keep the proposal need the creation model of the last
  run and say so otherwise. Open line comments go with a start that writes
  code and stay open with "Check and review".
- **Paused at a plan limit.** When a stage's subscription plan (Claude, Google
  AI or the one pi signs in with) reaches its usage limit, the run pauses
  instead of failing: the Agent run card reads "Paused until the … plan's limit
  resets at 15:30" (or "Paused: the … plan reached its usage limit" when the CLI
  gave no time, and "the limit has reset, resume the run" once it has), the
  stage that stopped reads Paused, the task list shows **Paused** with the
  reason, and nothing half done is kept. **Resume agents** opens the same
  confirmation with the start that continues the run chosen: **Continue the
  review** (keeps the change and its passing checks), **Write new tests**, or
  **Write the change again**. Another model can be chosen for the stage in the
  role settings before resuming.
- **Watch.** The Conversation shows each stage with its model and duration and
  each specialist's verdict and findings. A running stage spins; a stage that
  waits on someone (your review, or a paused stage waiting for a plan's reset)
  shows a yellow hourglass instead. **Stop agents** asks first and applies
  nothing.
- **Code rules.** Under the brief, a Code rules card lists each task file with
  budgets from its `AGENTS.md` files: its lines against the file budget, what
  the proposal made go over (the complexity gate fails), what was already over
  and may not grow (tracked as a Maintainability issue), functions the parser
  could not measure, and any rules block that was not applied. A file over its
  budget after Creation or Testing is split by a fix-up step; new sibling files
  join the scope as new files, and the Conversation says what moved and which
  functions changed while moving. Function complexity defaults to 15, so the
  card appears for any task unless the guides turn every budget off.
- **Spend.** Each stage of the agent run shows the tokens it spent and its
  estimated cost, the task's details show its total, and Settings → Usage
  lists every task's spend with the total. Estimates come from the daemon's
  price table; a missing rate shows a lower bound or no cost, never zero. A run
  that finishes is announced with its spend.
- **Revise.** A revision request goes to the Code profile (remote consent when
  needed) and creates a new version: earlier view marks, checks and approval no
  longer apply, and the last agent run reads outdated until the agents run
  again. While the agent works, the revise box shows how long it has
  been working and **Stop**, which asks first, cancels the model call and keeps
  the current version.
- **Stale.** A task is stale only when a file it depends on changed: its
  targets and read context, the other files of the target packages, or a build
  manifest above them. The task then reads Stale, names the changed file and
  offers no Apply and no run; changes elsewhere keep the task current.
- **Re-analyze.** A stale task's primary action. After one confirmation (and
  remote consent for a remote Bug model) a model checks whether the issue is
  still present in the current files. Still valid: the task is captured again
  at the current source with its title, plan, criteria, scope and linked
  issues; its old proposal, checks, approval, comments and conversation are
  dropped, a note records the decision, and the agents can start. Not valid:
  the task is deleted and its linked issues are dismissed; the task list opens.
  The model's reason is shown either way. **Fix with agents** links the task to
  its issue so this, Apply & commit and merging update the issue.
- **Live progress.** A task's stages and the end of its run update the page and
  the task lists as they happen, in its own worktree as in the main checkout.

## Review and apply

- **View files.** Each changed file is marked viewed at the current proposal
  hash (`v` marks it and moves to the next unviewed file). "Since last review"
  shows only what changed since the last complete review.
- **Line comments.** Any line of a changed file takes a comment (the gutter
  `+`); a comment writes nothing and changes no review state. Open comments
  can be deleted, show on their file in the tree, and go to the agents as one
  repair brief with the next revision (**Revise with N comments**, or the
  revise box's "Include my line comments") or agent run; sent comments are
  kept as history.
- **Checks.** Running checks needs execution trust and runs in an isolated copy.
  Only required checks gate approval; an optional failure is a warning. When
  the project changed after checks ran, they read Outdated and must run again
  before approval or Apply. Tests and lint run per subproject the task touches
  (“tests: web”); the others are listed as not affected. A check whose failures
  all happened before the change too passes, labeled “Only failures from before
  the change”; one with new failures names them; one that also failed before but
  whose failures could not be told apart still blocks (“Also failed before the
  change”).
- **Reviewer findings.** Specialists separate blocking findings from advisory
  notes; only findings request changes, and notes are shown apart from them.
- **Override reviewers.** When the reviewers rejected the current version and
  its checks pass, the human may override them with a reason (up to 1024
  bytes). The verdict stays on record, the override is shown on the run and
  binds this exact hash, and the task then waits for the human's own review.
  Nothing is approved; a human rejection cannot be overridden.
- **Approve.** Offered when every file is viewed, required checks pass and the
  workflow waits for the human. Approval binds this exact hash and answers a
  waiting workflow input; it writes nothing. Optional notes (up to 2048 bytes)
  stay with the approval and are cleared with it. **Reject proposal** ends a
  waiting workflow after confirmation.
- **Agents with tools.** When a stage ran as a tool-using agent (a task in its
  own worktree on a pi, Claude Code or agy model), its timeline line says "Worked with tools: N calls
  in M turns (read_file 12, …)", and "stopped before finishing" when the agent
  never called finish. The agent's report, with how each criterion is met, is its
  message in the conversation.
- **Task in its own worktree.** The proposal is the worktree's diff against the
  task's base commit; Files changed marks new, deleted and moved files. Edits
  made in the worktree outside Mini-Orca become a new version of the proposal
  (its checks and approval reset, and the conversation says so); files outside
  the task's scope join it, and its plan stays approved. Edits that cannot
  become the proposal (more files than a task holds) leave the task stale with
  the reason, never unopenable. A commit on
  the task branch by someone else makes the task stale. While agents run, the
  worktree is locked against removal. The gate reads **Ready to accept?**, and
  **Accept & commit** commits the approved diff on the task branch (no Undo:
  discard the worktree instead); re-analysis does not apply.
- **Apply.** Writes the approved version to its checkout, optionally followed
  by a commit with the shown message. The confirmation names the destination,
  the branch for a commit, and the running tasks in that checkout whose files
  it changes; Apply stops only those, and other tasks keep running.
- **Issues from the keyboard.** `j`/`k` select the next or previous issue and
  open its detail, `f` starts "Fix with agents", `x` dismisses or reopens it and
  `Esc` closes the detail. Chips filter by kind (bugs, performance, security).
  An issue shows its file and line, and the function around that line while
  the file still has the content the finding was made from; the source says
  where it came from (security rule, AI suggestion, parser, vet, test, rules).
- **Analysis everywhere.** While an analysis runs, the status bar shows its
  progress and the sidebar counts it; the code inspector shows how each stage
  went for the open file, with its reason, and links to the dashboard.
- **Delete a task.** The task page offers Delete (a button when the task is
  stale, an icon otherwise); the task list deletes every task of the "Failed or
  stale" or "Drafts" group at once, or the selected one with Delete. One
  confirmation names the tasks; project files never change. A worktree left
  with no tasks and no commits of its own is removed with its branch (as after
  a re-analysis that deletes the task), and the main checkout is selected again. Tasks with running
  agents, and the task whose latest Apply can still be undone, are kept.
- **Undo.** Covers only the latest Apply in a checkout and restores files only
  when they still match the applied version; a commit stays in history.

## Project

- **Open a project.** Restores saved data for a known path, otherwise imports
  and indexes it. No model request is made, and the project's own
  `.mini-orca/config.yaml` is applied when it has one.
- **Setup.** The first time a project without its own configuration opens, the
  Web UI opens Setup (once per window): Models (provider, model, address, key,
  reasoning effort, CLI path, with a connection test; "Same as Analyze" copies
  a scope), Agents & budget with time limits (saving writes the file and
  applies it at once),
  Checks (allow the detected commands) and Analysis (leave folders out, then
  finish or finish and analyze). Steps can be revisited from the step bar.
  "Skip" keeps the daemon's configuration and setup is not offered again; the
  Inbox offers it while it is needed and reports an invalid file, and Settings
  → Project shows where the configuration comes from and reopens setup, and
  Settings → Models & agents reopens it with **Change models**. Saving is
  refused while an analysis, agent work or worktree work runs.
- **Time limits.** Settings → Models & agents → Time limits edits how long each
  analysis request, project overview, Code model request, agent model call and
  check command may take, in seconds (1 to 86,400, with the current value as a
  duration). Save writes only those keys to the project's configuration and
  applies them at once, keeping agent settings; it is refused while work runs.
  Failures that timed out point here.
- **Set-aside files.** When the daemon set aside an unreadable agent journal,
  task file or saved analysis progress, or skipped one a newer Mini-Orca wrote,
  the Inbox says so and Settings → Project lists each file with where its bytes
  went and the remedy.
- **Agent models.** Settings → Models & agents chooses the model each role runs
  with (writes the change, writes tests, reviews): a configured scope or a model
  Claude Code, the Agy CLI or the Pi CLI lists, each marked local or remote, so
  each role can run on a different subscription. It also sets the budget
  per run within the `config.yaml` ceilings. Save applies to new runs only;
  **Use the configuration** resets. When the models change under saved settings,
  they are marked out of date and analysis and agent runs wait for a reset; the
  Inbox, the Analysis page and setup say so and offer **Reset**, and saving
  setup resets them. Remote models still
  ask before each run.
- **Trust.** Granting lists the commands checks will run; revoking stops checks
  and agent verification. Trust is bound to the canonical project path.
- **Analyze.** A preview comes first; the confirmation names the file count, the
  maximum model requests, remote destinations, whether a security review is
  included, and the checks that run after the files: the parser and code
  budgets of every file, then each command with its arguments (each
  subproject's lint and tests when checks may run project code, semgrep with the
  project's own rules when it is installed), or why lint and tests are left out.
  Analysis writes findings and the handbook, never source. A dispatch
  window covers up to 64 files and 30 minutes; a run that reaches either pauses
  and resumes from the dashboard.
- **Docs.** The components, the imports between them (the diagram), entry
  points, dependencies and runbook are read from the code on every visit. A
  component's summary is written by the analyze model from its files'
  summaries and says where it came from. An analysis run ends by summarizing
  again the components whose files changed and writing the overview again; its
  confirmation counts those requests. **Refresh docs** does the same without a
  run and always writes the overview again: it asks first (naming a remote
  model), runs in the background, one at a time, and the page shows it running,
  then what it wrote and kept or why it failed or was skipped. Coding agents
  start from these summaries too: the components a task's files are in and
  the components those import or are used by, marked as model-written.
- **Ask the code.** Docs → Ask the code (or the palette: **Ask the codebase**,
  or a question of three words or more typed into it) asks the Review role's
  model, which must run as an agent (Claude Code, Agy or Pi; otherwise the page
  says so). It asks first, naming the model and that file contents leave the
  machine, then reads the code with read-only tools and answers with cited
  lines the daemon checked; each citation opens the file at its line in Code.
  Nothing is written; this window keeps its answers until it reloads.
- **Analysis dashboard.** Shows the last or running run live: overall progress,
  failed stages grouped by reason (with a link to the models when the provider
  could not be reached), each section's coverage and findings (opening Issues
  filtered to that category), the checks with each one's state and issues (the
  count opens Issues), the models used, and every file's stages with
  their reasons, filterable to failed, with findings or in progress. Controls
  follow the run: pause and cancel while it runs; resume (after a fresh preview)
  or cancel when it is paused or interrupted; otherwise analyze changes, retry
  only failed, stale and canceled stages (**Retry failed**, or **Retry canceled**
  after a canceled run), or re-analyze everything. A retry still covers the
  whole project: saved results show as done from the moment it starts, only the
  retried stages run, canceling it leaves the saved results done, and the
  confirmation says how many files are retried and kept.
- **Issues.** Dismiss and reopen are local triage. **Fix with agents** opens the
  composer with the task the daemon drafts from the finding (its file, that
  file's conventional test in its language, the kind and “done when” criteria);
  nothing starts until the user does. An issue without a file, such as a
  project-wide suggestion, drafts no files, so the composer asks for them; a
  long title is shortened to its first sentence (the description keeps it whole).
  Once a task links an issue, the issue leaves **Open** for **In progress**
  (with its count), where **Open task** replaces Fix with agents; it is marked
  fixed when the task is merged and returns to Open when the task is deleted or
  its worktree discarded. The Code page keeps marking it until then.
- **Explain a file.** Sends the file's admitted context to a model after remote
  consent; the explanation is transient and cannot be applied.

## Branches

- A worktree can merge only when every task on it is applied and committed and
  the main checkout is clean; otherwise the page says which condition is missing.
  A worktree whose commits the main branch already has reads **No new commits**
  (none yet) or **Merged**.
- **Delete the worktree and its branch after merging** (on by default, kept per
  browser) applies to every merge. Off, a merged worktree and its branch stay;
  **Remove worktree** deletes a clean worktree with nothing left to merge and
  all its tasks applied, together with its branch, after confirmation; when it
  has applied tasks (merged with this setting off, or merged in another Git
  tool) their linked issues are marked done, as a merge would.
- Merging needs execution trust (Git can run filters and merge drivers). The
  merge preview lists commits and the diff; the confirmation names overlapping
  paths. Overlap is a review hint, not a proven conflict.
- Conflicts stop the merge and leave it for the user's Git tool; **Abort merge**
  restores the branch after confirmation. A merged worktree's linked issues are
  marked done.
- **Checks after the merge.** A completed merge runs the tests and lint of the
  subprojects it changed on a copy of the merged checkout, in the background;
  the Branches page shows them as **Checks after merging <branch>** (Passed,
  Failed or Could not run) until dismissed, and a toast reports the outcome.
  Failures that already happened before the merge do not fail it. A failure
  never undoes the merge: the card says to fix it in a new task or revert the
  merge commit.
