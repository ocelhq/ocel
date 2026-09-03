import { defineConfig } from "vitest/config";
import { selectedTarget } from "./src/targets";

const target = selectedTarget();

export default defineConfig({
  test: {
    environment: "node",
    include: ["tests/**/*.journey.test.ts"],
    reporters: ["default", new URL("./src/reporter.ts", import.meta.url).pathname],
    globalSetup: [new URL("./src/globalSetup.ts", import.meta.url).pathname],
    testTimeout: target.legTimeoutMs,
    hookTimeout: target.legTimeoutMs,
    fileParallelism: true,
    maxWorkers: target.concurrency,
    maxConcurrency: 1,
    server: {
      deps: {
        inline: ["@ocel-tests/shared", "@console/auth", "@console/db"],
      },
    },
  },
});
