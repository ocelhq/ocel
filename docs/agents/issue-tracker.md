# Issue tracker: Beads (bd)

Issues and PRDs for this repo live in the local beads (`bd`) issue tracker, not GitHub Issues (even though `origin` is a GitHub remote). Run `bd prime` for the full workflow reference; this file covers what the engineering skills need.

## Conventions

- **Create an issue**: `bd create --title="..." --description="..." --type=task|bug|feature|chore|epic|decision|spike|story --priority=0-4`
- **Read an issue**: `bd show <id>`
- **List issues**: `bd list --status=open` / `bd list --status=in_progress`, or `bd search <query>` / `bd query` for text search
- **Comment on an issue**: `bd comment <id> "..."`
- **Apply / remove labels**: `bd label add <id> <name>` / `bd label remove <id> <name>`
- **Close**: `bd close <id> --reason="..."`

## Pull requests as a triage surface

N/A — beads has no PR concept. `/triage` should only ever process `bd` issues for this repo.

## When a skill says "publish to the issue tracker"

Run `bd create ...`.

## When a skill says "fetch the relevant ticket"

Run `bd show <id>`.

## Filing a child under an epic that carries a spec

An epic whose description states user stories, Implementation Decisions or an Out of
Scope list is the spec of record for everything filed beneath it. Review waves generate
findings whether or not there is scope for them, so a new child must earn its place
before it earns `ready-for-agent`:

- **Cite the authority.** Name the user story, the Implementation Decision, or the
  acceptance criterion the bead serves. A bead that cannot name one is proposing new
  scope, not completing existing scope — say so in the description and let a human rule.
- **Read the Out of Scope list first.** A bead that restates something already listed
  there is closed, not built.
- **A recorded decision is not a task.** "This is deliberate, X was preferred to Y"
  belongs in an ADR or the epic's notes, not in an open bead.
- **Style findings and invented refactors do not become beads.** Fix them in the change
  that introduced them, or leave them.
- **A test seam the epic's Testing Decisions did not charter is new scope.** So is a new
  test environment or runner.
- **Keep strays out.** A defect found while working an epic, but not about that epic's
  subject, is filed with no parent or under its own.

The same test applies when re-reviewing an epic: grade every open child against the spec
and close what the spec never asked for, with the violated clause quoted in the reason.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single bd issue (`--type=epic`) with **child** issues linked via `--parent`.

- **Map**: `bd create --title="<effort>" --type=epic --description="Notes / Decisions-so-far / Fog"`.
- **Child ticket**: `bd create --title="..." --type=task --parent=<map-id>` (inherits parent labels). Label with `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`) via `bd label add`.
- **Blocking**: `bd dep add <child-id> <blocker-id>` — beads' native dependency graph (equivalent shorthand: `bd dep <blocker-id> --blocks <child-id>`). A ticket unblocks automatically when every blocker closes.
- **Frontier query**: `bd children <map-id>` filtered to open, unblocked tickets (or `bd ready`, scoped to the map's children) — first in creation order wins.
- **Claim**: `bd update <id> --claim` — the session's first write.
- **Resolve**: `bd comment <id> "<answer>"`, then `bd close <id>`, then append a context pointer to the map's Decisions-so-far via `bd comment <map-id> "..."`.
