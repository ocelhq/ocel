# List Projects for the active organization

Status: ready-for-agent

## Parent

`.scratch/project-management/PRD.md`

## What to build

`GET /api/projects` — returns all Projects belonging to the caller's active-session organization, unpaginated. Reuses the session/membership helper and authorization approach established in the create/read-back slice.

- Scoped to `session.activeOrganizationId`, with membership independently verified.
- Returns only Projects in that organization — never Projects from other orgs the caller may or may not belong to.
- `401` if unauthenticated or no active organization.

Tests exercise the endpoint through the route-handler seam.

## Acceptance criteria

- [ ] `GET /api/projects` returns every Project in the caller's active organization and none from other organizations.
- [ ] Returns an empty collection (not an error) when the active org has no Projects.
- [ ] `401` when unauthenticated or when there is no active organization.
- [ ] Tests cover: multiple Projects listed for the active org, isolation from another org's Projects, empty result, and the unauthenticated case — through the route seam.

## Blocked by

- `.scratch/project-management/issues/02-create-and-read-back-project.md`
