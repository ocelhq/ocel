import express from "express";
import { orders } from "../ocel/index";

const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3402);

app.get("/health", (_req, res) => {
  res.json({ ok: true });
});

app.post("/orders", async (req, res) => {
  const { sku } = req.body ?? {};
  if (typeof sku !== "string" || sku.length === 0) {
    res.status(400).json({ error: "sku is required" });
    return;
  }
  const { rows } = await orders.query(
    "INSERT INTO orders (sku) VALUES ($1) RETURNING id, sku, placed_at",
    [sku],
  );
  res.status(201).json(rows[0]);
});

app.get("/orders", async (_req, res) => {
  const { rows } = await orders.query(
    "SELECT id, sku, placed_at FROM orders ORDER BY id",
  );
  res.json(rows);
});

app.listen(PORT, () => {
  console.log(`with-pulumi example listening on http://localhost:${PORT}`);
});
