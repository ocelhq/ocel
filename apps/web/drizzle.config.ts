import { defineConfig } from "drizzle-kit";

export default defineConfig({
  schema: "./db/schema/auth-schema.ts",
  out: "./drizzle",
  dialect: "postgresql",
  dbCredentials: {
    url: process.env.DATABASE_URL as string,
  },
});
