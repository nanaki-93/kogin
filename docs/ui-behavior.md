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
- Remote model use, check execution, Accept, merges, trust and stopping
  agents each ask first and name their exact effect. A request is never sent
  twice behind the user's back, and one mutation runs at a time.
- Unknown, unavailable, stale, partial, failed and canceled states keep their
  meaning; model suggestions are never shown as verified results.
- Pages follow the daemon's event stream; when it drops, the status bar says
  so and the stream reconnects with backoff.
- Text is kept short where it is written: the daemon clips a plan's problem,
  approach, root cause, alternatives, risks and verdict notes and a reviewer
  item's detail to word budgets, and the UI shows the reasoning behind a plan
  (**Why this plan**) and a hunk note's **How** behind disclosures. Prose runs
  no wider than `--measure` (68 characters).
- Settings → **Appearance** chooses the theme and dark palette, the
  **Density** (Comfortable, the default, reads a step larger; Compact fits
  more rows and lines), the **Font** (the system's own face or Inter, bundled
  with the UI) and the diff layout. Density and font are applied as
  `data-density` and `data-font` on the document, saved with the other
  preferences and kept across reloads.

## Tasks

- **Create.** A task needs a title; scope (up to eight files) and "done when"
  criteria are explicit. A file that does not exist yet joins the scope by
  typing its path (**New file**); agents can write only the files in scope,
  and a run whose proposal writes another file fails naming it. **Save
  draft** stores it without model work. The composer has no kind picker: a
  new task is a feature, and a task drafted from an issue keeps its issue's
  kind (fix, security, performance). Repairs are always on (up to two). When
  the agents cannot start yet, the reason stands beside **Start agents**, and
  an empty scope is outlined once the task has a title.
- **A task from a sentence.** In the command palette, `>` and a sentence
  ("> retry loop in the llm client ignores cancellation") draft a task from
  the words: the open issue it describes (with its file, test file and
  criteria, through the issue's own draft), the files it names, a title.
  **Start agents on it** creates the task and starts the run; **Edit it first**
  opens the composer. Nothing runs before one is chosen, and the remote-model
  confirmation still applies.
- **Next up.** The Inbox opens with the one thing to do now: a review that is
  ready, then a run that failed or went stale, a branch ready to merge, and
  the worst open issue. `Enter` starts it; **Skip** (`s`) shows the next.
- **Own worktree.** Every task gets its own branch (`orca/<title>-<suffix>`)
  and worktree from the main checkout's HEAD, so several tasks run and commit
  in parallel; the task then opens in that checkout. It is made from the main
  checkout even while a worktree is selected. It needs Git, a commit and trust,
  and the task's files must be committed (the daemon names a file that is
  not); until then the composer says what is missing and makes no task. The
  worktree runs checks under the main checkout's
  trust when its commands are the same, and with the agent roles and budget
  saved here; otherwise a warning says what to set. A task page shows its own
  checkout, but the workspace pages (sidebar, `g` keys and the Tasks crumb)
  keep the checkout chosen in the top bar, so opening a task in its own
  worktree does not switch Issues or Analysis to it.
- **Start agents.** Requires a trusted project (a testing agent on a CLI model
  creates the test files it needs; the daemon refuses the run when the testing
  model cannot). When something is missing, the composer says what instead of
  leaving the button silently disabled. The
  confirmation lists every remote destination the Creation, Testing and Review
  roles will use; local-only runs say so. Repairs run automatically, at most
  two. A security task adds a security specialist.
- **Learning log.** Docs → Learning log is what the project has taught so
  far: one entry per accepted task — the problem it removed and the approach,
  from its approved plan — with the concepts its change used as chips; a
  concept chip filters the log to every time it was met, with its count, and
  **Explain “…” in this project** hands Ask the code a question about it. On
  Ask the code, **Save to log** keeps an answer there. Hunk notes show the
  same concept chips during review.
- **Guided review.** When the walkthrough ordered the hunks (each note's
  kind and step), the one-hunk-at-a-time review follows that order across
  files — the problem's cause and fix first, then the supporting changes,
  then the tests — reading **Step n of m** with a chip for the hunk's part
  (Cause, Fix, Supports the fix, Proof), and, when the task has an approved
  investigated plan, the plan's problem pinned above the steps; the changed
  files list marks where each file comes in ("step 2"). Hunks the
  walkthrough did not order follow in file order; without steps the review
  reads in file order as before.
- **The plan gate.** With the project profile's **Plan approval** on (every
  task by default, or only grouped fixes), a run that starts from Create on a
  fresh proposal first investigates: the creation model reads the code with
  the read-only tools and proposes a plan — the problem, its root cause with
  cited lines (each a link to the code), the approach, the files it touches,
  rejected alternatives, risks and, for a grouped fix, a verdict per issue.
  The run stops at **Plan ready** (the task list shows it under Needs you,
  the Inbox's Next up reads "Decide on the plan" with **Open the plan**) and
  the task page is about that decision: the header's primary action is
  **Approve & run agents**, the **Proposed plan** card above the brief
  carries the same button and **Redirect…** in its head (the note box opens
  under it), the review gate puts a **Plan — Needs your approval** step
  first with "After you approve the plan" under Agents finished, and the
  revision box is not offered. Approving approves that exact plan and starts
  the run (the confirmation says the approval covers the displayed plan);
  redirecting takes a note and runs the investigation again, proposing a
  revised plan that waits again. No code is written, nothing to roll back:
  rejecting a plan costs one read-only run. The Investigation appears as its
  own timeline entry (Investigate & plan) with its model, usage and tool
  calls, and stays there on the approved run; while it runs, the gate reads
  "The investigation is reading the code to propose a plan." The composer
  and a fresh task's **Investigate & plan** button say the plan comes first
  whenever the profile's gate applies, and the composer's **Skip the plan
  this time** starts straight at the code for that one task.
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
- **Waiting for another task.** A run that would change code another open
  task is changing (it holds a proposal or an accepted commit not merged yet)
  waits instead: the Agent run card reads "Waiting for “…”" with why and
  **Open that task**, and the task list shows **Waiting**. When that task is
  merged or deleted, the waiting task continues by itself on the updated code:
  it is opened again in a new worktree, its fixed issues carried over while
  their commits still apply, and its agents start. If checks must be allowed
  again first (after a merge), the card says so.
- **Nothing to fix.** A task whose issues need no change never ends as a
  failure. When the investigation finds that every linked issue does not hold,
  is already fixed or needs a task of its own, the plan card reads **Nothing
  to fix** and its decision is **Dismiss the issue** (or **Dismiss N issues**,
  **Close task** when none is dismissed) beside **I think it's real…**, which
  redirects the investigation with your note; no agents run. A coding agent
  that changes nothing must say whether the issue is already fixed or does
  not hold, and the run then ends **Nothing to fix** with its reason, as does
  a run of issue steps in which none fixed anything and some do not hold.
  Closing lists the issues it dismisses, deletes the task and removes its
  worktree; a dismissed issue can be reopened in Issues.
- **Testing decisions.** The testing agent's tests are its own: a coding repair
  may not change them, and a coder sure one is wrong disputes it once; the
  next testing run decides. Under Write and run tests, the task lists the
  disputed tests the testing agent decided on and its **follow-ups** (gaps the
  change cannot cover), each with **New task**.
- **Paused at a plan limit.** When a stage's subscription plan (Claude, Google
  AI or the one pi signs in with) reaches its usage limit, the run pauses
  instead of failing: the Agent run card reads "Paused until the … plan's limit
  resets at 15:30" (or "Paused: the … plan reached its usage limit" when the CLI
  gave no time, and "the limit has reset, resume the run" once it has), the
  stage that stopped reads Paused, the task list shows **Paused** with the
  reason, and nothing half done is kept. **Resume agents** opens the same
  confirmation with the start that continues the run chosen: **Continue the
  review** (keeps the change and its passing checks), **Write new tests**, or
  **Write the change again**. Under **Models for this run** it also picks the
  model of each stage that runs, starting from the roles': a model of the plan
  that reached its limit is marked "limit reached", so another (a configured
  scope or one Claude Code, agy or pi lists) can take the stage over now
  instead of waiting for the reset. A stage the chosen start keeps shows the
  model it ran with and keeps it. The choice is for this run only; Settings →
  Models & agents keeps the roles for new runs.
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
  offers no Accept and no run; changes elsewhere keep the task current.
- **Re-analyze.** A stale task's primary action. After one confirmation (and
  remote consent for a remote Bug model) a model checks whether the issue is
  still present in the current files. Still valid: the task is captured again
  at the current source with its title, plan, criteria, scope and linked
  issues; its old proposal, checks, approval, comments and conversation are
  dropped, a note records the decision, and the agents can start. Not valid:
  the task is deleted and its linked issues are dismissed; the task list opens.
  The model's reason is shown either way. **Fix with agents** links the task to
  its issue so this, Accept & commit and merging update the issue.
- **Main changed the task's files.** A task in its own worktree keeps its base
  commit, so a merge of another task on the same files does not make it stale.
  Its page then says which of its files the main branch changed since it
  started, finished or not, and offers **Re-analyze & retake** (not while its
  agents run). After one confirmation a model checks the issue against the main
  branch's files. Still valid: the task starts again in a new worktree from the
  main branch with its title, plan, criteria, scope and linked issues, and its
  page opens. Not valid: it is deleted and its linked issues dismissed. Either
  way the old task is deleted, and its worktree is removed or, when it holds
  work, offered for discarding. The agents never run on such a task: pressing
  Start, Run or Resume agents reads the task again first and, when the main
  branch changed its files, says so in the retake confirmation (**Not now**
  leaves the task as it is), so no stage runs on code that is gone. A run
  checks again before each stage and each issue step; when the main branch
  changed its files meanwhile, it stops there (the stages before it kept,
  the rest skipped), reads **Out of date** in the task lists with the reason,
  and its page offers the retake.
- **Live progress.** A task's stages and the end of its run update the page and
  the task lists as they happen. A running task's status names its stage:
  Fixing 2 of 5 (the issue a task that fixes its issues one at a time is on),
  Writing code, Repairing, Testing (Running checks for a run started at the
  checks) or Reviewing.

## Review and accept

- **Review by hunks.** Files changed opens one hunk at a time (**Hunks**, the
  default; **Whole file**, or `f`, shows each file's whole diff). Beside the
  hunk: its place ("Hunk 2 of 7", a dot per hunk, the file's hunks grouped),
  what the file's change is for (the walkthrough's summary, or the agent's
  when there is none, with Explain changes), the walkthrough's note for the
  hunk, **Looks right** (`a`) and **Comment** (`m`, on the hunk's first
  changed line). The notes stay beside the code unless the review itself is
  under 640px wide, then they go above it. `←`/`→` (or `j`/`k`) move between
  hunks across files. Once every hunk of a file looks right, the file is marked
  viewed. The hunks you called right are kept per task version in this browser;
  a new version starts over, like the files viewed.
- **Review bar.** Under the diff in both views: files viewed (a dot per file,
  click to open it), the checks and the reviewers' verdict, **Next unviewed**,
  and **Accept & commit**, which says what still blocks it ("2 files left",
  "checks first") until it can run. On the Files tab the header's next step
  is **Next unviewed file**; the review gate offers Accept only once it can run.
- **Agents at work.** While agents run, the Conversation opens with what the
  agent does now ("Creation agent is editing …"), its last tool calls (read,
  search, edit, checks; a failed call says why), the files it changed so far,
  the time, the calls and the stage's spend, and **Stop**. The calls come from
  Kogin's own tools as the agent makes them; the log covers the latest run
  since the daemon started.
- **View files.** Each changed file is marked viewed at the current proposal
  hash (`v` marks it and moves to the next unviewed file). "Since last review"
  shows only what changed since the last complete review.
- **Behavior changes.** When the reviewers list what the version changes for
  its users (flags, exit codes, output, formats, timing…), the review stage
  shows them ("N behavior changes", To accept / Accepted), and **Accept &
  commit** lists them with "I accept these behavior changes", which must be
  ticked before it commits.
- **What it fixes.** The review model's walkthrough reads with the diff,
  hunk by hunk: over the diff, one line on what the file's change fixes (it
  replaces the agent's own line on the file); above each hunk, heading the
  context that leads into it, a note with the issue it serves ("Issue 2 · …",
  or the criterion when the task has no issues; "Needed by the change" for a
  helper or fixture), the wrong behavior it removes, and **How** in a few
  words. A noted hunk stays visible even inside unchanged code; a note whose
  line the diff lacks is listed under the file's line. It is written after the
  reviewers on the version they reviewed; an older one says it is for an
  earlier version. **Explain changes** (or the refresh button) writes it again
  for this version, asking first when the review model is remote. The notes
  button in the file header hides them, and the choice is kept. The notes are
  advice, never review evidence.
- **Line comments.** Any line of a changed file takes a comment (the gutter
  `+`); a comment writes nothing and changes no review state. Open comments
  can be deleted, show on their file in the tree, and go to the agents as one
  repair brief with the next revision (**Revise with N comments**, or the
  revise box's "Include my line comments") or agent run; sent comments are
  kept as history.
- **Checks.** Running checks needs execution trust and runs in an isolated copy.
  Only required checks gate approval; an optional failure is a warning. When
  the project changed after checks ran, they read Outdated and must run again
  before approval or Accept. Tests and lint run per subproject the task touches
  (“tests: web”); the others are listed as not affected. A check whose failures
  all happened before the change too passes, labeled “Only failures from before
  the change”; one with new failures names them; one that also failed before but
  whose failures could not be told apart still blocks (“Also failed before the
  change”).
- **Reviewer findings.** Specialists separate blocking findings from advisory
  notes; only findings request changes, and notes are shown apart from them.
  A finding is a defect the change introduces or a criterion it leaves unmet;
  problems the code already had, or improvements beyond the task, are notes.
  Each reads as a few words with its place (`master.go:42`); the reason waits
  behind **Why**. The place opens the task's diff of that file with the line
  marked and scrolled to, or the file in Code when the proposal does not change
  it. A reviewer's summary shows its first sentence.
- **Override reviewers.** When the reviewers rejected the current version and
  its checks pass, the human may override them with a reason (up to 1024
  bytes). The verdict stays on record, the override is shown on the run and
  binds this exact hash, and the task then waits for the human's own review.
  Nothing is approved; a human rejection cannot be overridden.
- **Accept & commit.** Offered, as the task header's primary action on every
  tab, when every file is viewed, required checks pass and the workflow waits
  for the human. One confirmation, with the commit message and optional notes
  (up to 2048 bytes), records the approval of this exact hash (answering a
  waiting workflow input; the notes stay with it and are cleared with it) and
  commits it on the task branch. A version approved but not committed (the
  commit failed, or it was approved before Accept became one step) is only
  committed. After Accept the header offers **Merge from Branches**. The task
  lists show such a version as **Ready to accept**, in its own group, apart
  from **Needs review** (files still to view, with how many are viewed) and
  **Run checks**. **Reject proposal** ends a waiting workflow after
  confirmation.
- **Agents with tools.** When a stage ran as a tool-using agent (a task in its
  own worktree on a pi, Claude Code or agy model), its timeline line says "Worked with tools: N calls
  in M turns (read_file 12, …)", and "stopped before finishing" when the agent
  never called finish. The agent's report, with how each criterion is met, is its
  message in the conversation.
- **Task in its own worktree.** The proposal is the worktree's diff against the
  task's base commit; Files changed marks new, deleted and moved files. Edits
  made in the worktree outside Kogin become a new version of the proposal
  (its checks and approval reset, and the conversation says so); files outside
  the task's scope join it, and its plan stays approved. Edits that cannot
  become the proposal (more files than a task holds) leave the task stale with
  the reason, never unopenable. A commit on
  the task branch by someone else makes the task stale. While agents run, the
  worktree is locked against removal. The gate reads **Ready to accept?**, and
  **Accept & commit** commits the reviewed diff on the task branch with the
  shown message; nothing is written to files, so there is nothing to undo
  (discard the worktree instead); re-analysis does not apply.
- **Tasks from before worktrees.** A task made in the main checkout before every
  task got its own worktree opens read only: the gate says where it was made,
  and there is no run, revision, approval or Accept, only Delete. Kogin never
  writes a proposal into the main checkout.
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
  agents are kept. A
  worktree left with no tasks that still holds their work (uncommitted changes,
  or commits the main branch lacks, as after an aborted merge) is offered for
  **Discard worktree**: a second confirmation names what is lost; **Keep it**
  leaves it on Branches.

## Project

- **Open a project.** Restores saved data for a known path, otherwise imports
  and indexes it. No model request is made, and the project's own
  `.kogin/config.yaml` is applied when it has one.
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
  task file or saved analysis progress, or skipped one a newer Kogin wrote,
  the Inbox says so and Settings → Project lists each file with where its bytes
  went and the remedy. **Delete** on a set-aside file asks for confirmation,
  then deletes it and its record; skipped newer files have no Delete, since
  they need that Kogin. A record whose file is already gone is not listed.
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
  Analysis writes findings and the docs, never source. A dispatch
  window covers up to 64 files and 30 minutes; a run that reaches either pauses
  and resumes from the dashboard.
- **Project map.** Docs → Project map: every folder and file as a block sized
  by its lines, folders nested while they have room (three levels), colored by
  **Open issues** (the worst severity), **Analysis** (fresh, stale, failed,
  running, missing), **Tasks** (how many tasks changed it) or **Language**.
  Hovering a block names its lines, issues, analysis and tasks; a folder opens
  (zooms) on click, a file opens in Code; the trail and `Backspace` go up.
- **Docs.** A guide to the project, laid out like a docs site: a nav with
  Overview, Get started, Architecture, How it works (one page per
  walkthrough), Where to change (one page per usual change: the files and
  functions to touch, in order), Components (one page per component),
  Dependencies and Ask the code, with an ask box on top. The components, the
  imports between them (the diagram), entry points, commands and dependencies
  are read from the code on every visit. The overview's purpose and
  architecture (one `path: role` line per part, shown as a list), the
  walkthroughs, the change guide and component summaries are model-written;
  a page with such prose shows one **AI** mark beside its title. A step keeps
  only a file the project has and a function only where the file declares
  it, and opens that file at its line. In model-written prose a project file
  path, a `backticked` file or a component path is a link (to Code, or to
  the component's page), and a backticked name a step found opens its
  declaration in Code; other backticked names show as code. A component
  page lists its imports, what uses it, the walkthroughs and changes through
  it and its files, each with the first sentence of its saved summary; a
  responsibility that only repeats the summary is left out. When the overview
  is missing, failed or out of date the model-written pages say so. An
  analysis run ends by summarizing again the components whose files changed
  and writing the overview again; its confirmation counts those requests.
  **Refresh docs** does the same without a run and always writes the
  overview again: it asks first (naming a remote model), runs in the
  background, one at a time, and the page shows it running, then what it
  wrote and kept or why it failed or was skipped; a failed rewrite keeps the
  overview the docs already had. Coding agents start from
  these summaries too: the components a task's files are in and the
  components those import or are used by, marked as model-written.
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
  or cancel when it is paused or interrupted; otherwise one primary action,
  the next step: **Retry failed** (or **Retry canceled** after a canceled run)
  when stages failed or were canceled and the files have not changed since,
  else **Analyze changes**. **More** holds the other ways to run, each saying
  what it does: analyze changes, retry only failed, stale and canceled stages,
  or re-analyze every file. A run whose files, settings or models changed says
  so in plain words and points to Analyze changes. A retry still covers the
  whole project: saved results show as done from the moment it starts, only the
  retried stages run, canceling it leaves the saved results done, and the
  confirmation says how many files are retried and kept.
- **Issues.** Dismiss and reopen are local triage. Code nothing calls is one
  "Delete unused code in <file>" issue per file (source "Unused code"); its
  task deletes the functions and the tests that only call them, and issues the
  analysis found inside them are not listed. **Fix with agents** opens the
  composer with the task the daemon drafts from the finding (its file, that
  file's conventional test in its language, the kind and “done when” criteria);
  nothing starts until the user does. An issue without a file, such as a
  project-wide suggestion, drafts no files, so the composer asks for them; a
  long title is shortened to its first sentence. An issue reads as four short
  lines, **Problem**, **Impact**, **Fix** and **Verify** (backticked names as
  code), above its actions; the analysis' own words fold under **Full
  analysis** only when they say more, and a tool's output shows as is. The
  drafted task carries the same lines and where the issue is.
  **Counts** splits **Work** (the default: the issues the project profile
  counts) from **Below the finish line** and **Out of scope** (AI issues in
  test files, files that only build for platforms the project skips, and
  second looks at code that had not changed since its last analysis), each
  with its count. Issues out of the work are left out of the open-issue counts
  (navigation and Inbox) and of **Fix N together**; their detail says why they
  are not counted and links to the profile, and they can still be fixed. An issue
  other analysis passes also reported lists them under **Also reported**; one
  that repeats a dismissed issue is shown dismissed, saying which one, and can
  be reopened.
  Issues are grouped by file by default (files with the most severe issue
  first, a file's issues by severity and line; **By severity** lists them
  flat). A file with more than one open issue offers **Fix N together**: one
  task drafted for up to five of them, which links each, so merging it marks
  them all fixed. Such a task fixes them one at a time (the composer says so):
  the Creation stage lists each issue as Waiting, Fixing,
  Fixed (with its commit; "Already fixed by an earlier step" shares that
  step's commit), Does not hold (with the agent's reason; merging the task
  dismisses it, and you can reopen it in Issues) or Not fixed (with the
  reason; it is open again in Issues), and accepting the task keeps one commit
  per fixed issue. Testing, review and repairs after the steps work only on
  the fixed issues; the ones left out are not tested or asked for again. Files outside the fix's folders that its agent was allowed
  to change are listed under the stage as **Added to the fix**, each with why.
  Once a task links an issue, the issue leaves **Open** for **In progress**
  (with its count), where **Open task** replaces Fix with agents; it is marked
  fixed when the task is merged and returns to Open when the task is deleted or
  its worktree discarded. The Code page keeps marking it until then.
- **Ask about it.** The Code inspector's question about a file opens Docs →
  Ask the code with the question filled in; nothing is sent until it is asked.

## Branches

- A worktree can merge only when every task on it is applied and committed and
  the main checkout is clean; otherwise the page says which condition is missing.
  Uncommitted changes in the main checkout (any untracked file outside
  `.kogin/` and `.worktrees/` counts) are named on its card and on every
  worktree ("main has changes"), since no branch merges until they are
  committed, stashed or moved.
  A worktree whose commits the main branch already has reads **No new commits**
  (none yet) or **Merged**.
- **Delete the worktree and its branch after merging** (on by default, kept per
  browser) applies to every merge. Off, a merged worktree and its branch stay;
  **Remove worktree** deletes a clean worktree with nothing left to merge and
  all its tasks applied, together with its branch, after confirmation; when it
  has applied tasks (merged with this setting off, or merged in another Git
  tool) their linked issues are marked done, as a merge would.
- A worktree with no tasks left that still holds work (uncommitted changes, or
  commits the main branch lacks) cannot merge here; **Discard worktree** deletes
  it with that work and its branch after a confirmation that names what is lost.
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

## Health

Health (`g l`) answers whether the project is fit for its goal and whether
fixing converges, measured from the main checkout without a model.
**Finish line** says how many counted issues are left (by severity), whether
the tests pass on this version, and what is not counted, each linking to that
filter of Issues; it reads **Done for its goal** when nothing counted is open
and the tests pass. **Found and fixed** draws 14 days of issues first reported
and fixed (paired bars, a tooltip per day, a table view) with the week's
verdict: Converging, Not converging, Steady or Quiet. **Tests** shows the
latest run (Run tests, with execution trust, runs them in a copy with Go
coverage), coverage by package, code and test lines with their ratio, and the
directories whose tests are spread over many files. **Code** shows the
repeated share of the code and of the tests, the repeated blocks, functions
nothing calls and functions over budget. **Improve the health** lists small
tasks drafted from these (delete unused code, merge a repeated block, gather a
file's spread tests into as few files as the file line budget allows, split a
complex function, test a package with low coverage); **Draft task** opens one in the composer.

Settings → **Profile** edits the project profile: kind (a kind brings its
security level unless one was chosen), purpose, shape, the platforms it runs
on, the security level (with what it covers), the finish line and whether test
files are analyzed. Saving applies it to the issue list at once; the analysis
reads it from the files it analyzes next.
