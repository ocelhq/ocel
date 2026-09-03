import { defineConfig } from "vitest/config";
import { DEFAULT_DATABASE_URL, postgresLink } from "./src/env";
import { selectedTarget } from "./src/targets";

const databaseUrl = process.env.DATABASE_URL ?? DEFAULT_DATABASE_URL;
const target = selectedTarget();

export default defineConfig({
  test: {
    environment: "node",
    include: ["tests/**/*.journey.test.ts"],
    reporters: ["default", new URL("./src/reporter.ts", import.meta.url).pathname],
    testTimeout: target.legTimeoutMs,
    hookTimeout: target.legTimeoutMs,
    fileParallelism: true,
    maxWorkers: target.concurrency,
    maxConcurrency: 1,
    env: {
      DATABASE_URL: databaseUrl,
      OCEL_RESOURCE_POSTGRES_main:
        process.env.OCEL_RESOURCE_POSTGRES_main ?? postgresLink("main", databaseUrl),
    },
    server: {
      deps: {
        inline: ["@console/auth", "@console/db"],
      },
    },
  },
});
