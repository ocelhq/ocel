import { pg } from "../ocel/index";

// Run via `ocel run -- pnpm migrate`, so the SDK-resolved connection for
// postgres("main") is already injected into the environment.
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
  await pg.end();
  console.log("migrated: todos + documents tables ready");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
