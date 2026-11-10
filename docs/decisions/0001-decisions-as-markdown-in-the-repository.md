# 1. Design decisions live as Markdown in the repository

Status: accepted
Date: 2026-11-10

## Context

Kogin's design choices were being made inside tasks and chat sessions and
then only visible as code. The owner wants to make those choices himself and
to be able to read later why each was made.

## Options

1. Markdown files in the repository (`docs/decisions/NNNN-title.md`).
   Versioned and reviewed with the code, readable without Kogin, and readable
   by an investigating agent. Writing one is a commit, not a click.
2. Rows in the task history (`.kogin/kogin.db`), recorded when a plan is
   approved or redirected. Quick to record from the app. Not versioned with
   the code, invisible outside Kogin, lost with the `.kogin` folder.

## Decision

Option 1. The reasons to keep a decision are the reasons to keep code: it
has to survive the tool that produced it and be reviewable next to what it
changed.

## Consequences

- Kogin only reads the records (`GET /api/projects/current/decisions`); it
  never writes one.
- An investigation is told which records exist and reads those that bear on
  its task.
- Recording a decision takes a commit, so small choices will go unrecorded.
  That is accepted: the log is for choices that shape the system.

## Revisit when

Decisions need to be queried across several projects, or records are not
being written because a commit is too much friction.
