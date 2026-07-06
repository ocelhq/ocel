# Prefactor: schema-file convention + test harness

Status: ready-for-agent

## Parent

`.scratch/project-management/PRD.md`

## What to build

Enabling/prefactor work that every later slice depends on. No user-facing behavior ships here — the goal is to make the subsequent changes easy.

- Add `zod` and `uuidv7` as `apps/web` dependencies (neither exists today).
- Change the Drizzle schema wiring away from the single hardcoded generated-`auth-schema` path: introduce a schema barrel that re-exports the generated auth schema (and, going forward, additional app-domain schema files), and update the Drizzle config's `schema` path and the db client's schema import to use a glob/barrel instead. The generated auth schema file must remain untouched and regenerable (`pnpm --filter web auth:generate` must still overwrite only it).
- Stand up **Vitest** as the test runner (new dependency + config; the first tests in the repo) with a test harness that runs against a real Postgres test database (reuse the existing `docker-compose.yml` Postgres service) and can mint a real Better-Auth-issued session together with an organization via the actual `auth.api.signUpEmail` + `auth.api.createOrganization` flows (not mocked). Each test is responsible for creating and tearing down its own user/org/session rows — no shared fixtures.
- Add one smoke test that proves the harness can connect to the test DB and mint a session + organization.

## Acceptance criteria

- [ ] `zod` and `uuidv7` are installed in `apps/web`.
- [ ] Drizzle config and db client resolve schema via a barrel/glob; adding a new app-domain schema file requires no further wiring.
- [ ] Regenerating the auth schema does not clobber the barrel or any app-domain schema.
- [ ] `vitest` runs in `apps/web` and a smoke test passes against the real test Postgres.
- [ ] The smoke test mints a real Better Auth session + organization (no mocks) and tears its own data down.
- [ ] Biome lint/format passes on new/changed files.

## Blocked by

None - can start immediately.
