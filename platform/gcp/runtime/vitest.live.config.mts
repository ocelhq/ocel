import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/live/**/*.live.test.mts"],
    testTimeout: 120_000,
    hookTimeout: 120_000,
    fileParallelism: false,
  },
});
