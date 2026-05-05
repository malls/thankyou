# TYB-8: Add documentation discipline rule to CLAUDE.md and agents.md

Add a new subsection `### Documenting Shipped Work` to the `## Lattice` section of both [CLAUDE.md](../../CLAUDE.md) and [agents.md](../../agents.md), placed between `### Where Learnings Go` and `### Quick Reference`. Identical content in both files (agents.md mirrors CLAUDE.md's Lattice section).

## What the rule says

- Tasks that ship user-facing functionality or make load-bearing architectural decisions must update the relevant doc before being marked done. For most tasks that means [README.md](../../README.md); for project-wide conventions or agent-facing rules, that means CLAUDE.md / agents.md.
- **Document:** new endpoints/commands/env vars/config knobs; architecture decisions (chosen + rejected + *why*); anything a future reader would absorb faster from prose than from code.
- **Don't document:** implementation details visible in well-named code; task history (git log + Lattice events handle that); speculative/deferred work (link to the plan file instead).
- The review gate enforces this: the review sub-agent's checklist now includes "did this ship user-facing functionality or make a load-bearing decision? if so, is the relevant doc updated?" If not, route back as `implementation-level rework needed`.

## Acceptance criteria

1. New `### Documenting Shipped Work` section exists in CLAUDE.md, between `### Where Learnings Go` and `### Quick Reference`.
2. Same section exists in agents.md, same placement, byte-identical content.
3. The two files' Lattice sections remain in sync (other than the `# thankyou` header CLAUDE.md has at the top).
4. No other content in CLAUDE.md or agents.md is touched.
5. Commit message: `docs(lattice): require user-facing doc updates as a review-gate condition` or similar.

## Lifecycle

Pragmatic compaction for a paragraph-sized doc change: orchestrator writes the plan and edits directly (no Plan or Implement sub-agent spawn — the work is fully specified, fits in one screen, and spawning sub-agents for it is theater). Review sub-agent IS spawned cold to validate the rule reads correctly and the two files stay in sync.

## Out of scope

- Editing the existing `### The Review Gate` section to spell out the doc check in its 5-step list. The new section says the review gate enforces the rule; that's enough. Re-touching the review-gate steps would be creep.
