import { migrate, pg } from "../ocel/index";

async function main() {
  await migrate();
  await pg.end();
  console.log("migrated: todos table ready");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
