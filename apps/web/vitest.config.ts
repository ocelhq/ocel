import path from "node:path";
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
    },
  },
  test: {
    environment: "node",
    // Route-level DB/auth tests now live in @repo/api alongside the
    // handlers they cover; apps/web keeps no database-backed tests itself.
    passWithNoTests: true,
  },
});
