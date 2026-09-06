import * as schema from "@console/db/schema";
import { pushSchema } from "drizzle-kit/api";
import { drizzle } from "drizzle-orm/node-postgres";
import { Pool } from "pg";

const DATABASE_EXISTS_CODES = new Set(["42P04", "23505"]);

async function ensureDatabaseExists(connectionString: string) {
  const url = new URL(connectionString);
  const dbName = url.pathname.slice(1);
  url.pathname = "/postgres";

  const adminPool = new Pool({ connectionString: url.toString() });
  try {
    await adminPool.query(`CREATE DATABASE "${dbName}"`);
  } catch (error) {
    if (!DATABASE_EXISTS_CODES.has((error as { code?: string }).code ?? "")) {
      throw error;
    }
  } finally {
    await adminPool.end();
  }
}

const SCHEMA_PUSH_LOCK = 815_213;

let setupPromise: Promise<void> | undefined;

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
        const client = await pool.connect();
        try {
          await client.query("SELECT pg_advisory_lock($1)", [SCHEMA_PUSH_LOCK]);
          const pushDb = drizzle(pool);
          const { apply } = await pushSchema(schema, pushDb);
          await apply();
        } finally {
          await client.query("SELECT pg_advisory_unlock($1)", [SCHEMA_PUSH_LOCK]);
          client.release();
        }
      } finally {
        await pool.end();
      }
    })();
  }

  return setupPromise;
}
