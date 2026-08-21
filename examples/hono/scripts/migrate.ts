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
      thumbnail_key TEXT,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )
  `);
  await pg.query(
    "ALTER TABLE documents ADD COLUMN IF NOT EXISTS thumbnail_key TEXT",
  );
  await pg.end();
  console.log("migrated: todos + documents tables ready");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
