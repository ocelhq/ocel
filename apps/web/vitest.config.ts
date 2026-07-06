import path from "node:path";
import { loadEnv } from "vite";
import { defineConfig } from "vitest/config";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, __dirname, "");

  return {
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "."),
      },
    },
    test: {
      environment: "node",
      // Route the app's normal DATABASE_URL at the test database, so
      // importing @/lib/db and @/lib/auth under test connects to
      // TEST_DATABASE_URL instead of the real dev database.
      env: {
        ...env,
        DATABASE_URL: env.TEST_DATABASE_URL,
      },
    },
  };
});
