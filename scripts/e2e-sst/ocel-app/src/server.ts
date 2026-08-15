import express from "express";

import { orders } from "../ocel/index";

const MARKER = "ocel-e2e-sst:consumer:v1";

const app = express();

const PORT = Number(process.env.PORT ?? 3301);

app.get("/", (_req, res) => {
  res.type("text/plain").send(MARKER);
});

app.get("/link", (_req, res) => {
  const url = new URL(orders.connectionString);
  res.json({
    marker: MARKER,
    host: url.hostname,
    port: url.port,
    database: url.pathname.replace(/^\//, ""),
    hasPassword: url.password.length > 0,
  });
});

app.get("/link/query", async (_req, res) => {
  try {
    const result = await orders.query("select 1 as ok");
    res.json({ ok: result.rows[0]?.ok === 1 });
  } catch (err) {
    res.status(503).json({ ok: false, error: (err as Error).message });
  }
});

app.listen(PORT, () => {
  console.log(`${MARKER} listening on http://localhost:${PORT}`);
});
