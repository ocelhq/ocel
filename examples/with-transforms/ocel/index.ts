import { postgres } from "ocel/postgres";

export const pg = postgres("main");

export async function migrate() {
  await pg.query(`
    CREATE TABLE IF NOT EXISTS todos (
      id SERIAL PRIMARY KEY,
      title TEXT NOT NULL,
      done BOOLEAN NOT NULL DEFAULT false
    )
  `);
}
