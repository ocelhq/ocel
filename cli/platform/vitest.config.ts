import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    // Tracing + transpiling a real Express app is slower than a unit test.
    testTimeout: 30_000,
  },
});
