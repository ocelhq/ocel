# Domain docs: CONTEXT.md + 3 ADRs

Status: ready-for-agent

## Parent

`.scratch/project-management/PRD.md`

## What to build

Capture the domain language and the hard-to-reverse decisions made for the Projects feature. These are the first such docs in the repo (single-context layout per `docs/agents/domain.md`).

- **Root `CONTEXT.md`** (create it) with glossary entries — definitions only, no implementation detail — for:
  - **Organization** — the tenant boundary; owns Projects.
  - **Project** — a named, org-unique-slugged unit belonging to exactly one Organization, grouping the resources/applications deployed together.
  - **Active Organization** — the organization currently selected in a caller's session, used to scope create/list requests.
- **Three ADRs** under `docs/adr/` (sequential numbering from `0001`):
  1. UUIDv7 (via app-level library) for Project ids, over Postgres's default v4 — chosen for time-ordering/index locality; reversing later costs a data migration.
  2. By-id Project routes authorize against the Project's real `organizationId` rather than requiring `session.activeOrganizationId` to match — a deliberate mixed pattern a future reader might otherwise "fix" incorrectly.
  3. Any organization member (not just admin/owner) may create/update/delete Projects — a provisional MVP permission model, flagged for revisit once org roles/permissions are configured.

## Acceptance criteria

- [ ] `CONTEXT.md` exists at the repo root with the three glossary terms, definitions only (no implementation detail), following the project's CONTEXT format.
- [ ] `docs/adr/0001-*.md`, `0002-*.md`, `0003-*.md` exist, each recording the decision and why in a short form.
- [ ] ADR 3 explicitly notes it is provisional and to be revisited when roles/permissions land.
- [ ] Terms and decisions match what the implementation slices actually did (no drift).

## Blocked by

- `.scratch/project-management/issues/02-create-and-read-back-project.md`
