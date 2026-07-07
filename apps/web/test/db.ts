import * as schema from "@repo/db/schema";
import { pushSchema } from "drizzle-kit/api";
import { drizzle } from "drizzle-orm/node-postgres";
import { Pool } from "pg";

async function ensureDatabaseExists(connectionString: string) {
  const url = new URL(connectionString);
  const dbName = url.pathname.slice(1);
  url.pathname = "/postgres";

  const adminPool = new Pool({ connectionString: url.toString() });
  try {
    await adminPool.query(`CREATE DATABASE "${dbName}"`);
  } catch (error) {
    // 42P04 = duplicate_database - already exists, nothing to do.
    if ((error as { code?: string }).code !== "42P04") {
      throw error;
    }
  } finally {
    await adminPool.end();
  }
}

let setupPromise: Promise<void> | undefined;

// Ensures the test database exists and its tables match the current
// Drizzle schema (via drizzle-kit's migrationless push API), without
// requiring committed migration files. Safe to call from multiple test
// files - the underlying work only runs once per test process.
export function setupTestDatabase() {
  if (!setupPromise) {
    setupPromise = (async () => {
      const connectionString = process.env.DATABASE_URL;
      if (!connectionString) {
        throw new Error(
          "DATABASE_URL must be set (vitest.config.ts routes it to TEST_DATABASE_URL) to run tests against a real Postgres instance",
        );
      }

      await ensureDatabaseExists(connectionString);

      const pool = new Pool({ connectionString });
      try {
        // Not the app's @/lib/db instance: pushSchema needs a schema-less
        // PgDatabase generic, which the app's typed db doesn't satisfy.
        const pushDb = drizzle(pool);
        const { apply } = await pushSchema(schema, pushDb);
        await apply();
      } finally {
        await pool.end();
      }
    })();
  }

  return setupPromise;
}
