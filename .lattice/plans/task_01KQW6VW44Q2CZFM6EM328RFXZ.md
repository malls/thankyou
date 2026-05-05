# TYB-11: Commit Message Convention

## Scope
Add a `### Commit Messages` subsection to the Lattice section of both `agents.md` and `CLAUDE.md`. The two files mirror each other; keep them in sync.

## Convention to document
Any commit produced by an agent working on a Lattice task must be prefixed with the task's short ID (e.g., `TYB-11`). Form: `TYB-11: <conventional-commit-message>`. This makes the event log → git history mapping legible at a glance.

## Placement
Insert the new subsection immediately after `### Branch Linking` and before `### Leave Breadcrumbs`. Both deal with stitching Lattice state to git state, so they belong adjacent.

## Acceptance criteria
- New `### Commit Messages` subsection present in both files, identical content.
- States the rule, gives an example, explains *why* (traceability) in one line.
- This task's own commit follows the new convention (prefixed with `TYB-11:`).
