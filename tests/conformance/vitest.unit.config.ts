import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    include: ["tests/completeness.test.ts", "tests/links.test.ts"],
  },
});
