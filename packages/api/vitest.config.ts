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
