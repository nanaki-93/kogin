# Design decisions

Each file here records one design decision of Kogin: what forced it, the
options that were weighed, which one was chosen and why, what follows from
it, and what would make it the wrong choice.

- A record is `NNNN-short-title.md`, numbered in the order written.
- `Status:` is `proposed` while the options are being weighed, `accepted`
  once one is chosen, and `superseded by NNNN` when a later record replaces
  it. An accepted record is not rewritten: a new one supersedes it.
- A record is written and changed in an ordinary commit, with the code it is
  about when there is any.

Kogin reads these files for every project that has them (see
[ui-behavior.md](../ui-behavior.md), Docs → Decisions): the page lists them,
and each investigation is told they exist, so a plan that goes against an
accepted decision has to say so.

## How decisions are made here

A change that shapes how Kogin works (a new guard or a relaxed one, where
state lives, what runs without a person) is not decided inside a task.
Whoever proposes it, a person or an agent, writes a `proposed` record with
at least two real options and what each costs. The owner chooses, and the
record becomes `accepted` in the commit that carries out the choice.
