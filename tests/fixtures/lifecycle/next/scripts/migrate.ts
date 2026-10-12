import { pg } from "../infra/index";

async function main() {
  await pg.query(`
    CREATE TABLE IF NOT EXISTS todos (
      id    SERIAL PRIMARY KEY,
      title TEXT    NOT NULL,
      done  BOOLEAN NOT NULL DEFAULT false
    )
  `);
  await pg.query(`
    CREATE TABLE IF NOT EXISTS documents (
      id         SERIAL      PRIMARY KEY,
      key        TEXT        NOT NULL,
      name       TEXT        NOT NULL,
      mime_type  TEXT        NOT NULL,
      size       BIGINT      NOT NULL,
      owner_id   TEXT,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )
  `);
  await pg.query(`
    CREATE TABLE IF NOT EXISTS next_state (
      key        TEXT        PRIMARY KEY,
      count      INTEGER     NOT NULL DEFAULT 0,
      first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
      last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
    )
  `);
  await pg.query(
    "CREATE TABLE IF NOT EXISTS notes (id serial PRIMARY KEY, title text NOT NULL UNIQUE)",
  );
  await pg.query("INSERT INTO notes (title) VALUES ('first'), ('second') ON CONFLICT DO NOTHING");
  const { rows } = await pg.query<{ n: number }>("SELECT count(*)::int AS n FROM notes");
  await pg.end();
  console.log(`migrated: ${rows[0]?.n} notes, beside the todos, documents and next_state tables`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
