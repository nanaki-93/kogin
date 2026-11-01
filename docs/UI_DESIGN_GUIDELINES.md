# Mini-Orca UI guidelines

Mini-Orca's interface is the browser workspace (`web/`, served by the daemon at
`/ui`). It exists for three jobs, in this order:

1. **Start a task and watch the agents** — describe a change, give it a scope
   and "done when" criteria, start the agents and follow every stage live.
2. **Review and apply** — read every changed file, run checks, approve the exact
   version and apply it, with Undo for the latest Apply.
3. **Understand the project** — browse code, read findings with their evidence
   and read the generated docs.

Everything else (models, roles, budgets, skills, trust) is supporting
configuration and stays one step away from these jobs.
[ui-behavior.md](ui-behavior.md) is the behavior contract for every workflow;
this document owns how the interface is organized, looks and responds.

## Principles

- **Show the next step.** Every view answers "what needs me now?" first. Tasks
  that wait for review, failed runs and branches ready to merge rise to the top;
  the primary action for the selected item is always visible.
- **Detail beside the list.** Lists stay on the left; the selected item's detail
  stays on the right. Moving the selection updates the detail without leaving
  the list.
- **Nothing happens by looking.** Navigation, selection, filters, previews,
  disclosures and the command palette never call a model, run project code or
  write source. Every action that does names its effect before it happens.
- **States keep their meaning.** Unknown, unavailable, stale, partial, failed,
  canceled and "not run" are always labeled as such; an empty list never claims
  the project is clean, and model suggestions are never shown as verified.
- **One vocabulary.** Every page uses the same workspace names, task statuses
  and review gate, and a key does the same thing wherever it applies.

## Information architecture

| Workspace | Keys | Contents |
| --- | --- | --- |
| **Inbox** | `g h` | Needs your review, agents working, failed or stale, ready to merge, recently applied; open issues and analysis coverage beside them. |
| **Tasks** | `g t` | Every task grouped by what it needs, with filters and `j`/`k`/`Enter`. A task opens on its own page. |
| **Code** | `g c` | A read-only explorer: file tree, editor tabs, highlighted source with issue markers, an inspector. |
| **Issues** | `g i` | Findings with status, severity and category filters; the selected finding's problem, source and actions, then its evidence as short sections (preconditions, fix, how to verify). |
| **Analysis** | `g a` | The last or running analysis: progress, why stages failed, results per section, the models used and each file's stages; analyze, retry failed, pause, resume and cancel. |
| **Docs** | `g d` | A guide to the project: overview, get started (commands, entry points), architecture (components diagram), how it works (walkthroughs), a page per component with its files, dependencies, and Ask the code. |
| **Branches** | `g b` | The main checkout and task worktrees with their tasks, integration state, merge preview and merge (removing the worktree and branch unless switched off), and removal of a worktree with nothing left to merge. |
| **Setup** | — | The project's first-run wizard: models, agents & budget, checks, analysis. Opens once for a project without its own configuration; Settings → Project reopens it. |
| **Settings** | `g s` | Project, trust, models and agents, analysis, usage, appearance. |

### Task status

A task is always in exactly one of these states, shown with the same glyph and
label everywhere:

| Glyph | Label | Meaning |
| --- | --- | --- |
| filled ring | Needs review / Changes requested / Revised | Agents finished, or the proposal was revised after their run; the human decides (Revised: run the agents again). |
| spinner | Agents working / Canceling | A run is active. |
| red ring | Failed / Interrupted | The run stopped; the reason and recovery are on the task. |
| dotted ring | Stale | A file the task depends on changed; re-analyze it (refreshed if still valid, otherwise deleted). |
| dashed ring | Draft | No run yet. |
| check | Applied / Committed | Written to the checkout. |
| struck ring | Canceled / Undone | Ended without applying. |

### Review gate

Apply is offered only when every condition holds, and the gate lists them in
this order with their current state: **Agents finished** (a run of this
version that reached a verdict; a revision after it does not count),
**Reviewers** (the recorded verdict on this version, else the specialists'
verdicts; "Overridden by you" after an override), **Files viewed** (every
changed file viewed at the current proposal hash), **Checks** (every required
check passed) and **Your approval** of this exact version. The gate is derived
from daemon state; the daemon re-checks every condition when Apply is requested.
A failed check that is not required reads as a warning, never as a pass or a
blocker.

## Shell

- **Sidebar** — project switcher, New task, the workspaces with counts,
  Settings and collapse. Collapsed, it keeps icons with tooltips.
- **Top bar** — breadcrumbs that start with the checkout menu (main checkout
  or an agent worktree; every page then shows that checkout) followed by the
  page and, on a task, the task title; then the command palette trigger
  (`⌘K` / `Ctrl+K`).
- **Content panel** — pages sit in one panel inset from the window chrome
  (sidebar and gutters use `--chrome`, the panel `--bg`).
- **Status bar** — connection to the live event stream, busy work, running
  agents, trust, models, project path, version and the shortcut reference.

Pages update from the daemon's event stream; a lost stream reconnects with
backoff and the status bar says so. Light, dark and system themes share one
token set; keep text at 4.5:1 contrast or better against its surface. The dark
theme comes in palettes (Midnight, Graphite, Ocean, Forest, Plum, Ember, True
black), chosen in Settings → Appearance or the command palette; a palette
(`data-palette` in `styles/tokens.css`) replaces only surfaces, text and the
accent, never status colors. Text on the accent uses `--on-accent`, so a light
accent stays readable.

## Pages

- **Inbox** — a one-line summary of what waits for you, grouped task rows and
  branches ready to merge, with open issues and analysis coverage beside them.
- **Tasks** — grouped list with status filters that count their tasks and
  `j`/`k`/`Enter`; an empty filter or search says so and offers the way on. Rows share
  fixed trailing columns (status, kind, files, time) so they line up across
  groups.
- **Task** — Conversation (brief, criteria, scope, the agent timeline with
  reviewer verdicts, messages, revise), Files changed (file tree with viewed
  marks, progress and comment counts, unified or split diff with line
  comments, "since last review"), Checks
  (results with output), and the Ready to apply? rail with Apply, commit message
  and Undo.
- **Code** — virtualized file tree, editor tabs per checkout, highlighted
  source with issue markers and line links, an inspector with Explain, "New
  task with this file", one sentence on what the file does (the rest of its
  summary on demand) and the docs component it belongs to.
- **Issues**, **Docs**, **Branches** (merge preview with commits and diff, merge
  and abort; a worktree that cannot merge yet says why) and **Settings** follow
  the same list-and-detail structure.

### Task composer

Title, optional description, kind (fix, feature, performance, security), the
files agents may change (up to eight, searched or typed as a new file's path; a
companion test is suggested for every Go file and a test file is required to
start agents) and "done when" criteria.
**Save draft** and **Start agents** (`⌘↵`) are explicit; starting lists every
remote model destination for consent first.

## Keyboard

| Keys | Action |
| --- | --- |
| `⌘K` / `Ctrl+K`, `/` | Command palette: files, tasks, issues, commands |
| `⌘P` / `Ctrl+P` | Go to file |
| `c` | New task |
| `g` then `h t c i d b s` | Go to a workspace |
| `j` / `k`, `Enter` | Move through and open list rows |
| `v`, `j` / `k`, `s` | Files changed: mark viewed, next/previous file, split diff |
| `⌘↵` / `Ctrl+↵` | Start agents in the composer; send a revision |
| `?` | Keyboard shortcuts |

Single-key shortcuts never fire while typing, under an open dialog or with a
modifier (`usePageKeys` and `useShortcuts` own those rules).

## Implementation rules

- React + TypeScript; server state through TanStack Query only; one hash
  router whose `wt` parameter carries the selected checkout.
- Strict CSP: same-origin scripts, styles and fonts, no inline script, and one
  per-response nonce for the style tag dialogs inject. Never add inline styles
  through HTML strings; React style props are set through the CSSOM.
- Every request carries the Web UI marker header; mutations carry the write
  token from the document and the guarded proposal identity; failures show the
  daemon's user message. In development the Vite proxy adds the token from the
  daemon's token file.
- Shared pieces live in `components/` (primitives in `ui.tsx`, domain pieces in
  `domain.tsx`); a page does not grow its own copy of a list group, status badge
  or key handler.

## Accessibility

- Color always pairs with a glyph or a word.
- Every interactive element is reachable by keyboard and shows a focus ring;
  dialogs, menus, tabs and tooltips use Radix primitives.
- Text stays readable at the smallest supported size: nothing that explains an
  action or a failure is truncated without a way to read it in full.

## Verification

`make webui-test` (type-check and vitest), `make webui-e2e` (Playwright: the
real UI against the stateful fake daemon in `web/e2e/support`, failing on any
console error, in both themes), `make webui-dist-check`,
`go test ./internal/api/handlers ./cmd/daemon`, and a look at changed pages in
dark and light themes. Keep the fake daemon's data in the daemon's real shapes
and states; add a scenario there when a page depends on a state it lacks.
