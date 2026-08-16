import { defineConfig } from "vitest/config";
import { postgresLink } from "./src/env";

export default defineConfig({
  test: {
    environment: "node",
    include: ["tests/**/*.dev.test.ts"],
    globalSetup: ["./src/globalSetup.ts"],
    testTimeout: 120_000,
    hookTimeout: 180_000,
    fileParallelism: true,
    env: {
      DATABASE_URL:
        process.env.DATABASE_URL ??
        "postgres://postgres:postgres@localhost:5432/postgres",
      OCEL_RESOURCE_POSTGRES_main:
        process.env.OCEL_RESOURCE_POSTGRES_main ??
        postgresLink(
          "main",
          process.env.DATABASE_URL ??
            "postgres://postgres:postgres@localhost:5432/postgres",
        ),
    },
    server: {
      deps: {
        inline: ["@console/auth", "@console/db"],
      },
    },
  },
});
