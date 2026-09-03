import { DEFAULT_DATABASE_URL, postgresLink } from "@ocel-tests/shared/env";
import { defineConfig } from "vitest/config";

const databaseUrl = process.env.DATABASE_URL ?? DEFAULT_DATABASE_URL;

export default defineConfig({
  test: {
    environment: "node",
    include: ["tests/**/*.dev.test.ts"],
    globalSetup: ["./src/globalSetup.ts"],
    testTimeout: 120_000,
    hookTimeout: 180_000,
    fileParallelism: true,
    env: {
      DATABASE_URL: databaseUrl,
      OCEL_RESOURCE_POSTGRES_main:
        process.env.OCEL_RESOURCE_POSTGRES_main ?? postgresLink("main", databaseUrl),
    },
    server: {
      deps: {
        inline: ["@ocel-tests/shared", "@console/auth", "@console/db"],
      },
    },
  },
});
