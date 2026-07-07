import { defineConfig } from "drizzle-kit";

export default defineConfig({
  schema: "./src/schema/index.ts",
  out: "./drizzle",
  dialect: "postgresql",
  dbCredentials: {
    // Matches the root docker-compose.yml Postgres, so `db:push` works
    // against local dev with zero setup; export DATABASE_URL to override.
    url:
      process.env.DATABASE_URL ??
      "postgres://postgres:postgres@localhost:5432/postgres",
  },
});
