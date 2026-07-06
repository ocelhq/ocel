# Delete a Project + org-cascade

Status: ready-for-agent

## Parent

`.scratch/project-management/PRD.md`

## What to build

`DELETE /api/projects/{id}` — hard delete (row removed, no soft-delete/archive). Authorized the same way as get-by-id: against the Project's own `organizationId`, independent of the session's active org.

- `404` for a non-existent Project or one in an org the caller doesn't belong to (don't leak existence).
- Also verify the schema-level cascade: deleting an Organization removes all of its Projects, leaving no orphaned rows.

Tests exercise the endpoint through the route-handler seam, plus a test for the org-deletion cascade.

## Acceptance criteria

- [ ] `DELETE /api/projects/{id}` permanently removes the Project and a subsequent get-by-id returns `404`.
- [ ] `404` for a non-existent id and for a Project in an org the caller doesn't belong to.
- [ ] A member of the Project's org can delete it regardless of which org is active in the session.
- [ ] Deleting an Organization cascades to delete all its Projects (verified in a test).
- [ ] Tests cover: successful delete + follow-up 404, non-member 404, and org-cascade — through the route seam / DB assertions.

## Blocked by

- `.scratch/project-management/issues/02-create-and-read-back-project.md`
