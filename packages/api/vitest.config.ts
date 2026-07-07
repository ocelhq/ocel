import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    // Matches the root docker-compose.yml Postgres, so tests run against a
    // real database with zero setup; export TEST_DATABASE_URL to override.
    env: {
      DATABASE_URL:
        process.env.TEST_DATABASE_URL ??
        "postgres://postgres:postgres@localhost:5432/ocelhq_test",
      // @repo/db dogfoods the ocel SDK (packages/resources) for its own
      // connection, which reads this var instead of DATABASE_URL directly -
      // normally injected by `ocel dev`. Point it at the same test database
      // so `pnpm test` works standalone.
      OCEL_RESOURCE_POSTGRES_main: JSON.stringify({
        connectionString:
          process.env.TEST_DATABASE_URL ??
          "postgres://postgres:postgres@localhost:5432/ocelhq_test",
      }),
      // The local "cloud" cluster's admin connection - a docker-compose
      // Postgres standing in for Aurora Serverless v2 in prod (see the
      // epic's design decisions). Locally there's only one Postgres
      // instance, so this points at the same one as DATABASE_URL/
      // TEST_DATABASE_URL but authenticated as its superuser.
      OCEL_CLOUD_ADMIN_URL:
        process.env.TEST_OCEL_CLOUD_ADMIN_URL ??
        process.env.TEST_DATABASE_URL?.replace(/\/[^/]+$/, "/postgres") ??
        "postgres://postgres:postgres@localhost:5432/postgres",
    },
    server: {
      // @repo/auth and @repo/db ship raw TS with no build step, so Vite
      // must transform them instead of externalizing as prebuilt deps.
      deps: {
        inline: ["@repo/auth", "@repo/db"],
      },
    },
  },
});
