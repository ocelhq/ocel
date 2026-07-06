# AGENTS.md

Ocel is a monorepo: a Go CLI/dev-server, a TypeScript SDK (`ocel` npm package),
and a Next.js dashboard (`apps/web`), all wired together by protobuf
definitions in `proto/`. The project is early-stage — several CLI commands
are intentionally stubs (see "Current state" below).

## Rules
- When working on a feature, commit after each passing test. Avoid large diffs.
- Flag assumptions instead of presenting them as settled.
- If a file is untracked and you're unsure of its purpose, confirm before deletion.

## Layout

- `proto/` — single source of truth API definitions (buf, v2 config).
- `ocel/` — Go module (`github.com/ocelhq/ocel`), the `ocel` CLI (cobra).
  Entry point `ocel/cmd/ocel/main.go` -> `ocel/internal/cli`.
- `packages/ocel/` — the `ocel` npm package: end-user SDK (e.g. `ocel/postgres`)
  used inside apps managed by Ocel.
- `packages/native-lib/` — per-platform npm packages that bundle prebuilt
  `ocel` Go binaries; `packages/ocel/bin/run.js` picks the right one at
  runtime via `optionalDependencies`. Don't expect these to build from source.
- `apps/web/` — Next.js dashboard. Also acts as the "Ocel API" server for
  `ocel login` (Better Auth device-authorization flow) and (eventually)
  resource provisioning.
- `packages/db/`, `packages/resources/` — currently empty, untracked, reserved
  for future use.

## Codegen (proto -> Go / TS)

Generated code **is committed to git** (not gitignored) — regenerate after
any `.proto` change, don't hand-edit the generated files:

```
pnpm gen
```

This runs `buf generate --template buf.gen.go.yaml` (proto -> `ocel/pkg/proto/...`,
Go structs + Connect service) then `buf generate --template buf.gen.ts.yaml`
(proto -> `packages/ocel/src/gen/proto/...`, protobuf-es). Requires the `buf`
CLI (available via `pnpm exec buf`, declared as a devDependency) and a Go
toolchain with the `tool` directives in `ocel/go.mod` resolvable.

## Go commands

`go.work` only declares `./ocel`, so root-level `go build ./...` fails with
"directory prefix . does not contain modules listed in go.work". Either run
Go commands from inside `ocel/` (`cd ocel && go build ./...`) or scope the
pattern from root: `go build ./ocel/...`.

## Node / pnpm commands

Workspace = `apps/*` + `packages/*` (pnpm). Pinned package manager:
pnpm 11.10.0 (see `devEngines` in root `package.json`).

- `apps/web`: `pnpm --filter web dev|build|start`, `pnpm --filter web lint`
  (Biome, not ESLint), `pnpm --filter web format`.
- `packages/ocel`: `pnpm --filter ocel build` (`tsc`, emits to `dist/`,
  `dist/` is gitignored — never hand-edit it).
- There is **no root-level lint/typecheck/test script** and no CI config yet;
  Biome config only exists under `apps/web/biome.json`.
- No automated tests exist anywhere in the repo currently.

## Database (apps/web)

Drizzle + Postgres, config in `apps/web/drizzle.config.ts` (schema:
`db/schema/auth-schema.ts`, reads `DATABASE_URL`).

- `pnpm --filter web db:generate|db:push|db:studio`
- `pnpm --filter web auth:generate` regenerates `db/schema/auth-schema.ts`
  from the Better Auth config in `lib/auth.ts` — re-run this after changing
  auth plugins/config, don't hand-edit that schema file.
- Local Postgres: `docker compose up -d` (root `docker-compose.yml`, exposes
  `localhost:5432`, user/password `postgres`).

## Env vars — two unrelated `.env` files

- Root `.env` only holds `AWS_BEARER_TOKEN_BEDROCK` for OpenCode's own Bedrock
  provider (see `opencode.json`) — it is **not** application config.
- App config lives in `apps/web/.env.local` (see `apps/web/.env.example` for
  the full list: `BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`,
  `NEXT_PUBLIC_BETTER_AUTH_URL`, `DATABASE_URL`, `GITHUB_CLIENT_ID/SECRET`).

## SDK <-> dev-server resource convention

`packages/ocel` resource helpers (e.g. `postgres(id)`) read their live config
from an env var named `OCEL_RESOURCE_<TYPE>_<id>` (e.g.
`OCEL_RESOURCE_POSTGRES_main`), expected to contain JSON with a
`connectionString` field (see `packages/ocel/src/utils/get-config.ts`). When
`OCEL_PHASE=discovery` is set, constructing a resource instead makes it
self-register via a Connect RPC call to `OCEL_DEV_SERVER` (the local `ocel dev`
server, default port `:8080`, see `ocel/internal/cli/dev.go`). This
discovery -> sync -> inject-env-var pipeline is only partially implemented —
`ocel dev` and `ocel init` currently print "not implemented yet" and exit.

## Auth flow context

`ocel login` uses Better Auth's device-authorization plugin against
`apps/web` (default `http://localhost:3000`, override with `--api-url` or
`OCEL_API_URL`). There is no deployed Ocel server yet, so during development
the dashboard must be running locally (`pnpm --filter web dev`) for
`ocel login` to work. Client id is the constant `ocel-cli`
(`apps/web/lib/constants.ts`).
