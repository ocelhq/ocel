import { pg } from "../ocel/index";

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
  await pg.end();
  console.log("migrated: todos + documents + next_state tables ready");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
