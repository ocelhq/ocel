# Create + read-back a Project (tracer bullet)

Status: ready-for-agent

## Parent

`.scratch/project-management/PRD.md`

## What to build

The thinnest complete path through every layer: create a Project and fetch it back by id. This establishes the `project` table, the shared session/membership helper, request validation, and the first two secured endpoints.

- **`project` table** (in an app-domain schema file separate from the generated auth schema):
  - `id`: UUIDv7, generated in application code via `uuidv7` (not Postgres v4 default, not Better Auth's id generator).
  - `organizationId`: FK -> `organization.id`, `onDelete: cascade`.
  - `name`: text, 1-100 chars.
  - `slug`: text, matches `^[a-z0-9]+(-[a-z0-9]+)*$`, 1-63 chars. Unique per `(organizationId, slug)` — NOT globally unique.
  - `description`: text, nullable.
  - `createdBy`: FK -> `user.id`, nullable, `onDelete: set null`.
  - `createdAt` / `updatedAt`: standard timestamps.
- **Session/membership helper** (first of its kind): resolves the current Better Auth session from request headers, reads the caller's active organization id, and independently verifies membership rows rather than trusting `session.activeOrganizationId` at face value (that column has no FK constraint).
- **zod create schema** enforcing the name and slug rules above.
- **`POST /api/projects`** — creates a Project scoped to the caller's active-session organization; `createdBy` is the acting user. Any organization member may call. `201` on success; `400` on validation failure; `409` on slug conflict within the org; `401` if unauthenticated or no active organization.
- **`GET /api/projects/{id}`** — returns a single Project, authorized against the Project's own `organizationId` (independent of the session's currently-active org). `404` both when the Project doesn't exist and when the caller isn't a member of its org (don't leak existence).

Tests exercise this through the route-handler seam (invoke exported handlers with constructed `Request` objects), asserting status codes, response bodies, and persisted DB state — not internal calls.

## Acceptance criteria

- [ ] `project` table exists with the fields, `(organizationId, slug)` uniqueness, and cascade/set-null FK behavior above; migration generated.
- [ ] `POST /api/projects` creates a Project in the caller's active org with a UUIDv7 id and `createdBy` set to the acting user, returning `201`.
- [ ] Two different organizations can each hold a Project with the same slug; a duplicate slug within one org returns `409`.
- [ ] Invalid name/slug returns `400`; unauthenticated or no-active-org returns `401`.
- [ ] `GET /api/projects/{id}` returns the Project for a member of its org regardless of which org is active in the session.
- [ ] `GET /api/projects/{id}` returns `404` for a non-existent id and for a Project in an org the caller doesn't belong to.
- [ ] Tests cover create (success, validation error, slug conflict, cross-org slug reuse, unauthenticated) and get-by-id (success, cross-active-org access, non-member 404) through the route seam.

## Blocked by

- `.scratch/project-management/issues/01-prefactor-schema-convention-test-harness.md`
