import express from "express";
import { orders } from "../ocel/index";
import { env } from "../ocel/vars";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3401);

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = express();
app.use(express.json());

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: APP_NAME });
});

app.get("/ocel.svg", (_req, res) => {
  res.setHeader("content-type", "image/svg+xml");
  res.setHeader("content-length", String(OCEL_SVG_BYTES.byteLength));
  res.end(OCEL_SVG_BYTES);
});

app.get("/api/link", (_req, res) => {
  const url = new URL(orders.connectionString);
  res.json({
    host: url.hostname,
    port: Number(url.port),
    database: decodeURIComponent(url.pathname.slice(1)),
    hasPassword: url.password.length > 0,
    greeting: env.GREETING,
  });
});

app.get("/api/link/query", async (_req, res) => {
  await orders.query("select 1");
  res.json({ ok: true });
});

app.listen(PORT, () => {
  console.log(`with-sst example listening on http://localhost:${PORT}`);
});
