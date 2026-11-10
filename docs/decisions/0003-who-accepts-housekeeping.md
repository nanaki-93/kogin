# 3. Who accepts housekeeping changes

Status: proposed
Date: 2026-11-10

## Context

A reviewed task that changed only test files, passed every required check,
was approved by the reviewers and stayed within what it was asked is listed
in the Inbox's Housekeeping group. Today the owner accepts the ready ones
together with one confirmation.

The daemon accepts a change only after a person approved that exact version:
`ApplyChange` requires the in-memory authority `review:<hash>` that the
review request sets, and a waiting run never approves an investigator plan
by itself. Fully unattended housekeeping would mean the daemon grants that
authority to itself.

## Options

1. Keep the confirmation. One click per batch, every accepted change still
   traces to a person. Housekeeping still needs the owner to be there.
2. A per-project opt-in (profile `housekeeping: autopilot`) under which the
   daemon accepts a task that meets the same conditions, records "accepted
   by autopilot" in the history, and never merges. No attention needed.
   Removes the rule that only a person approves; a wrong "ready" judgment
   lands as a commit on a task branch before anyone looks.
3. Option 2 with a delay: the task is listed as "will be accepted at <time>"
   and can be stopped until then. Keeps a window to object, adds a timer and
   a state to the workflow.

## Decision

Not decided.

## Revisit when

Housekeeping tasks pile up in the Inbox unaccepted, which would argue for
option 2 or 3; or one of them turns out wrong after being accepted together,
which would argue for keeping option 1.
