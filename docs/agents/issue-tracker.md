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

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single bd issue (`--type=epic`) with **child** issues linked via `--parent`.

- **Map**: `bd create --title="<effort>" --type=epic --description="Notes / Decisions-so-far / Fog"`.
- **Child ticket**: `bd create --title="..." --type=task --parent=<map-id>` (inherits parent labels). Label with `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`) via `bd label add`.
- **Blocking**: `bd dep add <child-id> <blocker-id>` — beads' native dependency graph (equivalent shorthand: `bd dep <blocker-id> --blocks <child-id>`). A ticket unblocks automatically when every blocker closes.
- **Frontier query**: `bd children <map-id>` filtered to open, unblocked tickets (or `bd ready`, scoped to the map's children) — first in creation order wins.
- **Claim**: `bd update <id> --claim` — the session's first write.
- **Resolve**: `bd comment <id> "<answer>"`, then `bd close <id>`, then append a context pointer to the map's Decisions-so-far via `bd comment <map-id> "..."`.
