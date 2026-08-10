import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    env: {
      DATABASE_URL:
        process.env.TEST_DATABASE_URL ??
        "postgres://postgres:postgres@localhost:5432/ocelhq_test",
      OCEL_RESOURCE_POSTGRES_main: JSON.stringify({
        connectionString:
          process.env.TEST_DATABASE_URL ??
          "postgres://postgres:postgres@localhost:5432/ocelhq_test",
      }),
      OCEL_CLOUD_ADMIN_URL:
        process.env.TEST_OCEL_CLOUD_ADMIN_URL ??
        process.env.TEST_DATABASE_URL?.replace(/\/[^/]+$/, "/postgres") ??
        "postgres://postgres:postgres@localhost:5432/postgres",
    },
    server: {
      deps: {
        inline: ["@repo/auth", "@repo/db"],
      },
    },
  },
});
