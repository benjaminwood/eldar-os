# 0001. Record decisions

Accepted, 2026-10-16

## Context

This system is built partly by AI agents working across many sessions, and
it's published for others to learn from. Without a written why, a later
session can quietly undo a choice that was made on purpose, and a reader
can't tell deliberate trade-offs from accidents.

## Decision

Keep short decision records in `docs/decisions/`, one per choice that shapes
the system. Each says what was decided, why, and what it costs, in a few
paragraphs a person can read in a minute. Code that exists because of a
decision links to it.

## Consequences

Changing a recorded decision means writing a new record, which makes the
change deliberate. Records stay small; detail belongs in the code and in
`docs/`.
