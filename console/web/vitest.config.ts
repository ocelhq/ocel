import path from "node:path";
import { defineConfig } from "vitest/config";

function postgresLink(name: string, url: string): string {
  const parsed = new URL(url);
  return JSON.stringify({
    name,
    postgres: {
      host: parsed.hostname,
      port: Number(parsed.port || 5432),
      database: parsed.pathname.slice(1),
      username: decodeURIComponent(parsed.username),
      password: decodeURIComponent(parsed.password),
    },
  });
}

export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "."),
    },
  },
  test: {
    environment: "node",
    passWithNoTests: true,
    env: {
      DATABASE_URL:
        process.env.TEST_DATABASE_URL ??
        "postgres://postgres:postgres@localhost:5432/ocelhq_test",
      OCEL_RESOURCE_POSTGRES_main: postgresLink(
        "main",
        process.env.TEST_DATABASE_URL ??
          "postgres://postgres:postgres@localhost:5432/ocelhq_test",
      ),
      OCEL_CLOUD_ADMIN_URL:
        process.env.TEST_OCEL_CLOUD_ADMIN_URL ??
        process.env.TEST_DATABASE_URL?.replace(/\/[^/]+$/, "/postgres") ??
        "postgres://postgres:postgres@localhost:5432/postgres",
    },
    server: {
      deps: {
        inline: ["@console/auth", "@console/db"],
      },
    },
  },
});
