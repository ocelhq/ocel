# Update a Project

Status: ready-for-agent

## Parent

`.scratch/project-management/PRD.md`

## What to build

`PATCH /api/projects/{id}` — partial update of a Project's `name`, `slug`, and/or `description`. Authorized the same way as get-by-id: against the Project's own `organizationId`, independent of the session's active org.

- zod update schema (partial) enforcing the same name (1-100 chars) and slug (`^[a-z0-9]+(-[a-z0-9]+)*$`, 1-63 chars) rules as create.
- Slug remains editable; a slug change is re-validated against the `(organizationId, slug)` uniqueness constraint and returns `409` on conflict within the org.
- `updatedAt` reflects the change.
- `400` on validation failure; `404` for a non-existent Project or one in an org the caller doesn't belong to (don't leak existence).

Tests exercise the endpoint through the route-handler seam.

## Acceptance criteria

- [ ] `PATCH /api/projects/{id}` updates any subset of name/slug/description and returns the updated Project.
- [ ] Changing the slug to one already used by another Project in the same org returns `409`.
- [ ] Changing the slug to one used only in a different org succeeds.
- [ ] Invalid name/slug returns `400`.
- [ ] `404` for a non-existent id and for a Project in an org the caller doesn't belong to.
- [ ] A member of the Project's org can update it regardless of which org is active in the session.
- [ ] Tests cover: successful partial updates, slug conflict, cross-org slug reuse allowed, validation error, and non-member 404 — through the route seam.

## Blocked by

- `.scratch/project-management/issues/02-create-and-read-back-project.md`
