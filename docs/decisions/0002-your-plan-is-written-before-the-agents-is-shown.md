# 2. Your plan is written before the agent's is shown

Status: accepted
Date: 2026-11-10

## Context

On pwd-manager, 31 changes were accepted with a median of two minutes
between the agents finishing and Accept. The plan gate gave a veto over the
agent's hypothesis but asked for none of the owner's own. Kogin is meant to
teach the project and the craft, and reading someone else's correct answer
teaches little.

## Options

When the owner's plan is written:

1. After the investigation, with the agent's plan hidden until it is written
   or skipped. The two plans are independent answers. The owner writes with
   no hint.
2. Before the investigation, in the composer, and the investigator reads it.
   The agent can answer the owner directly, but its plan is anchored on his
   and what it would have proposed alone is never seen.

How the two are compared:

1. Side by side, and the owner marks how close he was. No model call.
   Nobody tells him what he missed.
2. One more read-only model call writes what he got right and missed. More
   direct, but costs tokens on every task and adds model text to read and
   trust.

## Decision

After, hidden; side by side with the owner's own mark. Independence makes
the comparison honest, and finding the difference himself is the exercise.

## Consequences

- The plan is written once (`own_plan` on the task) and never edited; only
  the mark (same, partly, missed) changes.
- The daemon does not hide the investigator's plan; the Web UI does. The
  API still returns it.
- Skipping is allowed and recorded, so a comparison is never claimed where
  there was none.
- A redirected plan does not ask again: the first one has been seen.

## Revisit when

The marks are mostly "missed it" with no sign of improving, which would say
the owner needs the explanation option 2 gives; or skipping becomes the
habit.
