import { db } from "./infra/db";

await db.query(
  "CREATE TABLE IF NOT EXISTS notes (id serial PRIMARY KEY, title text NOT NULL UNIQUE)",
);
await db.query("INSERT INTO notes (title) VALUES ('first'), ('second') ON CONFLICT DO NOTHING");
const { rows } = await db.query<{ n: number }>("SELECT count(*)::int AS n FROM notes");
console.log(`migrated: ${rows[0]?.n} notes`);
await db.end();
