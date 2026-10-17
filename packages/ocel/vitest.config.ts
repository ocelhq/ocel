import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: {
      "#connect-transport": fileURLToPath(
        new URL("./src/runtime/connect-transport.ts", import.meta.url),
      ),
    },
  },
  test: {
    environment: "node",
    typecheck: { enabled: true, tsconfig: "./tsconfig.typecheck.json" },
    env: {
      OCEL_DEV_SERVER: "http://localhost:0",
      OCEL_PHASE: "discovery",
    },
  },
});
