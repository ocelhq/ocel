import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    typecheck: { enabled: true },
    env: {
      OCEL_DEV_SERVER: "http://localhost:0",
      OCEL_PHASE: "discovery",
    },
  },
});
