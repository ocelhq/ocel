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
  await pg.end();
  console.log("migrated: todos table ready");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
