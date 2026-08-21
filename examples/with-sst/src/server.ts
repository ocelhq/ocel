import express from "express";
import { orders } from "../ocel/index";

const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3401);

app.get("/api/health", (_req, res) => {
  res.json({ ok: true });
});

app.get("/api/link", async (_req, res) => {
  try {
    const url = new URL(orders.connectionString);
    const result = await orders.query("SELECT 1 AS connected");
    res.json({
      host: url.hostname,
      port: url.port,
      database: url.pathname.replace(/^\//, ""),
      hasPassword: url.password.length > 0,
      connected: result.rows[0]?.connected === 1,
    });
  } catch {
    res.status(503).json({ error: "link unavailable" });
  }
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
  console.log(`with-sst example listening on http://localhost:${PORT}`);
});
