import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    env: {
      // Discovery-phase declarations require OCEL_DEV_SERVER to be set
      // (see src/utils/defer.ts) even though the transport itself is
      // mocked out in these tests.
      OCEL_DEV_SERVER: "http://localhost:0",
      OCEL_PHASE: "discovery",
    },
  },
});
