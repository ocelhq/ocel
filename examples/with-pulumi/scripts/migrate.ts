import { orders } from "../ocel/index";

async function main() {
  await orders.query(`
    CREATE TABLE IF NOT EXISTS orders (
      id        SERIAL      PRIMARY KEY,
      sku       TEXT        NOT NULL,
      placed_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )
  `);
  await orders.end();
  console.log("migrated: orders table ready");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
