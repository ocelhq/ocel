import path from "node:path";
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
    },
  },
  test: {
    environment: "node",
    // Route-level DB/auth tests for @repo/api's handlers live in @repo/api
    // itself. apps/web's own DB-backed tests are scoped to scripts/ - the
    // local-dev Bun harness's dev-only endpoints, which don't exist in
    // @repo/api.
    passWithNoTests: true,
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
