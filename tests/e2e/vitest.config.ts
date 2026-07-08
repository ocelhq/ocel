import { defineConfig } from "vitest/config";

// End-to-end suite: drives the built Go CLI against a running apps/web control
// plane and the docker-compose Postgres services. Unlike the unit suites this
// is slow and service-dependent - run it via the e2e workflow, not `pnpm test`.
export default defineConfig({
  test: {
    environment: "node",
    include: ["tests/**/*.e2e.test.ts"],
    globalSetup: ["./src/globalSetup.ts"],
    // ocel init/run/dev + real provisioning are slow; give each spec room.
    testTimeout: 120_000,
    hookTimeout: 180_000,
    // Each example binds its own port and provisions its own database, so the
    // spec files are independent and may run concurrently.
    fileParallelism: true,
    env: {
      DATABASE_URL:
        process.env.DATABASE_URL ??
        "postgres://postgres:postgres@localhost:5432/postgres",
      OCEL_RESOURCE_POSTGRES_main:
        process.env.OCEL_RESOURCE_POSTGRES_main ??
        JSON.stringify({
          connectionString:
            process.env.DATABASE_URL ??
            "postgres://postgres:postgres@localhost:5432/postgres",
        }),
    },
    server: {
      // @repo/auth and @repo/db ship raw TS with no build step, so Vite must
      // transform them instead of externalizing as prebuilt deps.
      deps: {
        inline: ["@repo/auth", "@repo/db"],
      },
    },
  },
});
